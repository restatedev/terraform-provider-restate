package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"strips a trailing slash", "https://api.us.restate.cloud/cloud/", "https://api.us.restate.cloud/cloud"},
		{"strips repeated trailing slashes", "https://api.us.restate.cloud/cloud///", "https://api.us.restate.cloud/cloud"},
		{"preserves a deliberate path prefix", "https://api.us.restate.cloud/cloud", "https://api.us.restate.cloud/cloud"},
		{"preserves a nested path prefix", "https://gateway.example/restate/cloud", "https://gateway.example/restate/cloud"},
		{"accepts a bare host", "https://api.us.restate.cloud", "https://api.us.restate.cloud"},
		{"preserves an explicit port", "https://env.restate.cloud:9070", "https://env.restate.cloud:9070"},
		{"trims surrounding whitespace", "  https://api.us.restate.cloud/cloud  ", "https://api.us.restate.cloud/cloud"},
		{"accepts loopback http", "http://127.0.0.1:9070", "http://127.0.0.1:9070"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeBaseURL(tt.in)
			if err != nil {
				t.Fatalf("NormalizeBaseURL(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeBaseURLRejectsUnusableValues(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"no scheme", "api.us.restate.cloud/cloud"},
		{"wrong scheme", "ftp://api.us.restate.cloud"},
		{"no host", "https:///cloud"},
		{"query string", "https://api.us.restate.cloud/cloud?tenant=x"},
		{"fragment", "https://api.us.restate.cloud/cloud#frag"},
		{"embedded credentials", "https://user:pass@api.us.restate.cloud/cloud"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := NormalizeBaseURL(tt.in); err == nil {
				t.Errorf("NormalizeBaseURL(%q) = %q, want an error", tt.in, got)
			}
		})
	}
}

// A base URL that embeds credentials must be rejected rather than normalized, and the rejection
// must not quote the credential back at the user.
func TestNormalizeBaseURLDoesNotEchoEmbeddedCredentials(t *testing.T) {
	_, err := NormalizeBaseURL("https://user:sup3rsecret@api.us.restate.cloud/cloud")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "sup3rsecret") {
		t.Errorf("error echoed the password: %v", err)
	}
}

func TestNewTransportRequiresHTTPSForRemoteHosts(t *testing.T) {
	_, err := NewTransport(TransportConfig{BaseURL: "http://api.us.restate.cloud/cloud", BearerToken: "k"})
	if err == nil {
		t.Fatal("plaintext http to a remote host was accepted")
	}
	if !strings.Contains(err.Error(), "allow_insecure_http") {
		t.Errorf("error does not name the override: %v", err)
	}
}

func TestNewTransportAllowsPlaintextForLoopbackAndWithOptIn(t *testing.T) {
	for _, tt := range []struct {
		name  string
		cfg   TransportConfig
		allow bool
	}{
		{"loopback ip", TransportConfig{BaseURL: "http://127.0.0.1:9070", BearerToken: "k"}, true},
		{"localhost", TransportConfig{BaseURL: "http://localhost:9070", BearerToken: "k"}, true},
		{"ipv6 loopback", TransportConfig{BaseURL: "http://[::1]:9070", BearerToken: "k"}, true},
		{"explicit override", TransportConfig{BaseURL: "http://restate.internal:9070", BearerToken: "k", AllowInsecureHTTP: true}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewTransport(tt.cfg); err != nil {
				t.Errorf("NewTransport: %v", err)
			}
		})
	}
}

func TestNewTransportRequiresABearerToken(t *testing.T) {
	if _, err := NewTransport(TransportConfig{BaseURL: "https://api.us.restate.cloud/cloud"}); err == nil {
		t.Fatal("a transport was built without a credential")
	}
}

func TestTransportSendsBearerTokenAndUserAgent(t *testing.T) {
	var gotAuth, gotAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAgent = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewTransport(TransportConfig{
		BaseURL:           server.URL,
		BearerToken:       "key_test_token",
		UserAgent:         "terraform-provider-restate/0.1.0",
		AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drain(resp)

	if want := "Bearer key_test_token"; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if want := "terraform-provider-restate/0.1.0"; gotAgent != want {
		t.Errorf("User-Agent = %q, want %q", gotAgent, want)
	}
}

func TestTransportDoesNotForwardCredentialsThroughRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	// Recorded atomically because the assertion below reads it from the test goroutine while the
	// handler writes it from the server's.
	var redirectedAuth atomic.Value
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedRequests.Add(1)
		redirectedAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, destination.URL+"/credential-sink", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	client, err := NewTransport(TransportConfig{
		BaseURL:           origin.URL,
		BearerToken:       "must-not-leave-origin",
		AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}

	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drain(resp)

	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusTemporaryRedirect)
	}
	if got := redirectedRequests.Load(); got != 0 {
		leaked, _ := redirectedAuth.Load().(string)
		t.Errorf("redirect destination received %d requests, want none; Authorization = %q", got, leaked)
	}
}

// The auth tripper must not mutate the caller's request, which is a documented RoundTripper
// requirement and matters because the retrying tripper below it replays the same *http.Request.
func TestAuthRoundTripperDoesNotMutateTheCallersRequest(t *testing.T) {
	rt := &authRoundTripper{token: "secret", userAgent: "ua", next: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	})}

	req, err := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	drain(resp)

	if req.Header.Get("Authorization") != "" {
		t.Error("the caller's request was mutated with an Authorization header")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// instantPolicy retries as configured but never actually sleeps, and records what it would have
// waited so backoff behaviour can be asserted without slowing the test suite.
func instantPolicy(maxAttempts int, waits *[]time.Duration) RetryPolicy {
	policy := DefaultRetryPolicy()
	policy.MaxAttempts = maxAttempts
	policy.randFloat64 = func() float64 { return 1 } // full backoff, deterministic
	policy.sleep = func(ctx context.Context, d time.Duration) error {
		if waits != nil {
			*waits = append(*waits, d)
		}
		return ctx.Err()
	}
	return policy
}

func TestRetriesA429ForEvenTheLeastRetryableOperation(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := testClient(t, server.URL, instantPolicy(4, nil))

	// RetryThrottleOnly is the default and the safest level: a create must still retry a 429,
	// because a throttled request was never acted on.
	resp, err := do(t, client, WithRetrySafety(context.Background(), RetryThrottleOnly), server.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drain(resp)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestDoesNotRetryA500ForANonIdempotentOperation(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := testClient(t, server.URL, instantPolicy(4, nil))
	resp, err := do(t, client, WithRetrySafety(context.Background(), RetryThrottleOnly), server.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drain(resp)

	// A 500 on a create may mean the object exists. Retrying would risk a duplicate, so the single
	// attempt is the correct, conservative outcome.
	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1: a 500 must not be retried for a non-idempotent operation", got)
	}
}

func TestRetriesA500ForAnIdempotentOperation(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := testClient(t, server.URL, instantPolicy(4, nil))
	resp, err := do(t, client, WithRetrySafety(context.Background(), RetryServerErrors), server.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drain(resp)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}

func TestNeverRetriesAValidationOrAuthenticationFailure(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity,
		// The server does not offer the operation, so asking again cannot help.
		http.StatusNotImplemented,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()

			client := testClient(t, server.URL, instantPolicy(4, nil))
			// Ask for the most permissive level; the status classification must still refuse.
			resp, err := do(t, client, WithRetrySafety(context.Background(), RetryTransportErrors), server.URL)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			drain(resp)

			if got := attempts.Load(); got != 1 {
				t.Errorf("attempts = %d, want 1", got)
			}
		})
	}
}

func TestRetriesATransportErrorOnlyForReads(t *testing.T) {
	for _, tt := range []struct {
		name         string
		safety       RetrySafety
		wantAttempts int32
	}{
		{"read", RetryTransportErrors, 3},
		{"idempotent write", RetryServerErrors, 1},
		{"create", RetryThrottleOnly, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var attempts atomic.Int32
			base := roundTripFunc(func(*http.Request) (*http.Response, error) {
				attempts.Add(1)
				return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
			})

			client := &http.Client{Transport: &retryRoundTripper{policy: instantPolicy(3, nil), next: base}}
			req, err := http.NewRequestWithContext(WithRetrySafety(context.Background(), tt.safety), http.MethodPost, "https://example.test", strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Do(req); err == nil {
				t.Fatal("expected a transport error")
			}
			if got := attempts.Load(); got != tt.wantAttempts {
				t.Errorf("attempts = %d, want %d", got, tt.wantAttempts)
			}
		})
	}
}

func TestReplaysTheRequestBodyOnRetry(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if len(bodies) < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := testClient(t, server.URL, instantPolicy(3, nil))
	req, err := http.NewRequestWithContext(
		WithRetrySafety(context.Background(), RetryThrottleOnly),
		http.MethodPost, server.URL, strings.NewReader(`{"environmentId":"env_1"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drain(resp)

	if len(bodies) != 2 {
		t.Fatalf("got %d attempts, want 2", len(bodies))
	}
	if bodies[0] != bodies[1] {
		t.Errorf("retried body differs: first %q, second %q", bodies[0], bodies[1])
	}
}

// A body that cannot be rewound must disable retrying rather than be replayed truncated.
func TestDoesNotRetryAnUnrewindableBody(t *testing.T) {
	var attempts atomic.Int32
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		return &http.Response{StatusCode: http.StatusTooManyRequests, Body: http.NoBody, Header: http.Header{}}, nil
	})

	req, err := http.NewRequestWithContext(
		WithRetrySafety(context.Background(), RetryTransportErrors),
		http.MethodPost, "https://example.test", io.NopCloser(strings.NewReader("stream")))
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = nil

	rt := &retryRoundTripper{policy: instantPolicy(4, nil), next: base}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	drain(resp)

	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

func TestHonoursRetryAfterOverExponentialBackoff(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 2 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var waits []time.Duration
	client := testClient(t, server.URL, instantPolicy(3, &waits))
	resp, err := do(t, client, context.Background(), server.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drain(resp)

	if len(waits) != 1 {
		t.Fatalf("got %d waits, want 1", len(waits))
	}
	if waits[0] != 2*time.Second {
		t.Errorf("wait = %v, want the server's 2s Retry-After", waits[0])
	}
}

// A hostile or broken server must not be able to park an apply for hours via Retry-After.
func TestClampsAnExcessiveRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "86400")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	var waits []time.Duration
	policy := instantPolicy(2, &waits)
	client := testClient(t, server.URL, policy)
	resp, err := do(t, client, context.Background(), server.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drain(resp)

	if len(waits) != 1 {
		t.Fatalf("got %d waits, want 1", len(waits))
	}
	if waits[0] > policy.maxDelay() {
		t.Errorf("wait = %v, want it clamped to at most %v", waits[0], policy.maxDelay())
	}
}

func TestRetriesA503WithRetryAfterEvenForACreate(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := testClient(t, server.URL, instantPolicy(3, nil))
	resp, err := do(t, client, WithRetrySafety(context.Background(), RetryThrottleOnly), server.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drain(resp)

	if got := attempts.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}

// A 503 without Retry-After is indistinguishable from any other server error, so a create must not
// retry it.
func TestDoesNotRetryABare503ForACreate(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := testClient(t, server.URL, instantPolicy(3, nil))
	resp, err := do(t, client, WithRetrySafety(context.Background(), RetryThrottleOnly), server.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drain(resp)

	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

func TestRetryStopsWhenTheContextIsCancelled(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	// A real policy, so the loop genuinely waits and the cancellation is what stops it.
	policy := DefaultRetryPolicy()
	policy.MaxAttempts = 10
	policy.BaseDelay = 50 * time.Millisecond
	client := testClient(t, server.URL, policy)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	if _, err := do(t, client, ctx, server.URL); err == nil {
		t.Fatal("expected a cancellation error")
	}
	if got := attempts.Load(); got >= 10 {
		t.Errorf("attempts = %d, want the loop to stop early on cancellation", got)
	}
}

func TestZeroMaxAttemptsMeansASingleAttempt(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := testClient(t, server.URL, RetryPolicy{})
	resp, err := do(t, client, context.Background(), server.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drain(resp)

	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	policy := RetryPolicy{
		MaxAttempts: 10,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    time.Second,
		randFloat64: func() float64 { return 1 },
	}
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		time.Second,
		time.Second,
	}
	for i, expected := range want {
		if got := policy.backoff(i + 1); got != expected {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, expected)
		}
	}
}

// Full jitter means the delay is uniform over [0, exponential). Without it, parallel Terraform
// resource operations that hit the same rate limit would retry in lockstep forever.
func TestBackoffAppliesJitter(t *testing.T) {
	policy := RetryPolicy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: time.Minute}
	policy.randFloat64 = func() float64 { return 0.25 }
	if got, want := policy.backoff(3), time.Second; got != want {
		t.Errorf("backoff(3) with 0.25 jitter = %v, want %v (a quarter of the 4s exponential)", got, want)
	}
	policy.randFloat64 = func() float64 { return 0 }
	if got := policy.backoff(3); got != 0 {
		t.Errorf("backoff(3) with zero jitter = %v, want 0", got)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		value  string
		want   time.Duration
		wantOK bool
	}{
		{"seconds", "30", 30 * time.Second, true},
		{"zero seconds", "0", 0, true},
		{"http date in the future", now.Add(90 * time.Second).UTC().Format(http.TimeFormat), 90 * time.Second, true},
		{"http date in the past", now.Add(-time.Hour).UTC().Format(http.TimeFormat), 0, true},
		{"absent", "", 0, false},
		{"garbage", "soon", 0, false},
		{"negative", "-5", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			if tt.value != "" {
				header.Set("Retry-After", tt.value)
			}
			got, ok := retryAfter(header, now)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("delay = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsRetryableTransportErrorIgnoresCancellation(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if isRetryableTransportError(err) {
			t.Errorf("%v was classified as retryable; cancellation is the caller's decision", err)
		}
	}
	if !isRetryableTransportError(io.ErrUnexpectedEOF) {
		t.Error("a truncated response was not classified as retryable")
	}
}

func TestRetrySafetyDefaultsToTheSafestLevel(t *testing.T) {
	if got := retrySafetyFrom(context.Background()); got != RetryThrottleOnly {
		t.Errorf("default retry safety = %v, want RetryThrottleOnly", got)
	}
}

func testClient(t *testing.T, baseURL string, policy RetryPolicy) *http.Client {
	t.Helper()
	client, err := NewTransport(TransportConfig{
		BaseURL:           baseURL,
		BearerToken:       "key_test",
		UserAgent:         "test",
		Retry:             policy,
		AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	return client
}

func do(t *testing.T, client *http.Client, ctx context.Context, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	return client.Do(req)
}
