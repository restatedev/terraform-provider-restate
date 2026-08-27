package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The generic helpers are exercised once here with synthetic types. Every endpoint adapter is
// contract-tested separately against its own fixtures.

type synthetic struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// testExecutor decodes a `{"code","message"}` error body, matching the Cloud contract's shape.
func testExecutor() *Executor {
	return &Executor{
		Service: ServiceCloud,
		DecodeError: func(status int, body []byte) (string, string) {
			var decoded struct {
				Code    json.Number `json:"code"`
				Message string      `json:"message"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil {
				return "", ""
			}
			return decoded.Code.String(), decoded.Message
		},
	}
}

// invokerFor returns an Invoker that posts to the given server, so tests exercise the real
// transport, retry, and status handling rather than a stub.
func invokerFor(t *testing.T, client *http.Client, url string) Invoker {
	t.Helper()
	return func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
		if err != nil {
			return nil, err
		}
		return client.Do(req)
	}
}

func TestExecuteDecodesASuccessBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"env_1","name":"prod"}`)
	}))
	defer server.Close()

	client := testClient(t, server.URL, RetryPolicy{})
	got, err := Execute[synthetic](context.Background(), testExecutor(), "Describe", RetryTransportErrors, invokerFor(t, client, server.URL))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.ID != "env_1" || got.Name != "prod" {
		t.Errorf("decoded %+v, want {env_1 prod}", got)
	}
}

// An operation whose contract declares no response body must not be turned into a decode error.
func TestExecuteToleratesAnEmptySuccessBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := testClient(t, server.URL, RetryPolicy{})
	got, err := Execute[synthetic](context.Background(), testExecutor(), "Delete", RetryServerErrors, invokerFor(t, client, server.URL))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != (synthetic{}) {
		t.Errorf("decoded %+v, want the zero value", got)
	}
}

func TestExecuteReportsAStructuredErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amzn-Requestid", "req-42")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":403,"message":"principal is not authorized to perform this operation"}`)
	}))
	defer server.Close()

	client := testClient(t, server.URL, RetryPolicy{})
	_, err := Execute[synthetic](context.Background(), testExecutor(), "DescribeEnvironment", RetryTransportErrors, invokerFor(t, client, server.URL))
	if err == nil {
		t.Fatal("expected an error")
	}

	apiErr, ok := AsError(err)
	if !ok {
		t.Fatalf("error is not a structured API error: %v", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d, want 403", apiErr.StatusCode)
	}
	if apiErr.Code != "403" {
		t.Errorf("Code = %q, want %q", apiErr.Code, "403")
	}
	if !strings.Contains(apiErr.Message, "not authorized") {
		t.Errorf("Message = %q, want the API's own message", apiErr.Message)
	}
	if apiErr.RequestID != "req-42" {
		t.Errorf("RequestID = %q, want req-42", apiErr.RequestID)
	}
	if apiErr.Operation != "DescribeEnvironment" {
		t.Errorf("Operation = %q", apiErr.Operation)
	}
	if !IsForbidden(err) {
		t.Error("IsForbidden did not recognise the error")
	}
}

// A malformed error body must still produce a usable error with the status intact, not a decode
// failure that hides which operation was rejected.
func TestExecuteToleratesAMalformedErrorBody(t *testing.T) {
	for _, body := range []string{"", "not json", "<html>502 Bad Gateway</html>", "null"} {
		t.Run(fmt.Sprintf("%q", body), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()

			client := testClient(t, server.URL, RetryPolicy{})
			_, err := Execute[synthetic](context.Background(), testExecutor(), "Describe", RetryThrottleOnly, invokerFor(t, client, server.URL))
			apiErr, ok := AsError(err)
			if !ok {
				t.Fatalf("error is not a structured API error: %v", err)
			}
			if apiErr.StatusCode != http.StatusBadGateway {
				t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
			}
		})
	}
}

// A body the server claims is a success but which is not the declared shape is a contract
// violation, and must be reported as such rather than yielding a half-populated resource.
func TestExecuteReportsAnUndecodableSuccessBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id": `)
	}))
	defer server.Close()

	client := testClient(t, server.URL, RetryPolicy{})
	_, err := Execute[synthetic](context.Background(), testExecutor(), "Describe", RetryTransportErrors, invokerFor(t, client, server.URL))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "could not be decoded") {
		t.Errorf("error does not explain the failure: %v", err)
	}
}

// Additive server changes must not break existing configurations, which is the compatibility
// promise the pinned-snapshot policy rests on.
func TestExecuteIgnoresUnknownResponseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"env_1","name":"prod","futureField":{"nested":true}}`)
	}))
	defer server.Close()

	client := testClient(t, server.URL, RetryPolicy{})
	got, err := Execute[synthetic](context.Background(), testExecutor(), "Describe", RetryTransportErrors, invokerFor(t, client, server.URL))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.ID != "env_1" {
		t.Errorf("decoded %+v", got)
	}
}

func TestExecuteBoundsTheResponseSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("a", 4096))
	}))
	defer server.Close()

	ex := testExecutor()
	ex.MaxResponseBytes = 128

	client := testClient(t, server.URL, RetryPolicy{})
	_, err := Execute[synthetic](context.Background(), ex, "Describe", RetryTransportErrors, invokerFor(t, client, server.URL))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "larger than the 128 byte limit") {
		t.Errorf("error does not name the limit: %v", err)
	}
}

// A body exactly at the limit is valid and must not be rejected off by one.
func TestExecuteAcceptsABodyAtTheLimit(t *testing.T) {
	body := `{"id":"x"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	ex := testExecutor()
	ex.MaxResponseBytes = int64(len(body))

	client := testClient(t, server.URL, RetryPolicy{})
	got, err := Execute[synthetic](context.Background(), ex, "Describe", RetryTransportErrors, invokerFor(t, client, server.URL))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.ID != "x" {
		t.Errorf("decoded %+v", got)
	}
}

func TestExecuteReportsATransportFailure(t *testing.T) {
	// A closed server, so connecting fails outright.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	client := testClient(t, url, RetryPolicy{})
	_, err := Execute[synthetic](context.Background(), testExecutor(), "Describe", RetryTransportErrors, invokerFor(t, client, url))
	apiErr, ok := AsError(err)
	if !ok {
		t.Fatalf("error is not a structured API error: %v", err)
	}
	if apiErr.StatusCode != 0 {
		t.Errorf("StatusCode = %d, want 0 for a failure with no response", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Message, "could not reach the API") {
		t.Errorf("Message = %q", apiErr.Message)
	}
	if !apiErr.Retryable {
		t.Error("a connection failure should be marked retryable")
	}
}

// A Terraform operation that times out must say so, rather than reporting a generic connection
// failure that sends the operator looking at the network.
func TestExecutePrefersTheCallersCancellationReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := testClient(t, server.URL, RetryPolicy{})
	_, err := Execute[synthetic](ctx, testExecutor(), "Describe", RetryTransportErrors, invokerFor(t, client, server.URL))
	apiErr, ok := AsError(err)
	if !ok {
		t.Fatalf("error is not a structured API error: %v", err)
	}
	if !strings.Contains(apiErr.Message, "cancelled") {
		t.Errorf("Message = %q, want the cancellation to be named", apiErr.Message)
	}
	if !errors.Is(err, context.Canceled) {
		t.Error("the cancellation cause was not preserved for errors.Is")
	}
}

// Errors must never carry the response body or request headers, because either can contain a
// bearer token, a Kafka password, or a user payload.
func TestExecuteNeverLeaksCredentialsIntoAnError(t *testing.T) {
	const secret = "key_live_do_not_log_me_1234567890"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// A server echoing the submitted token back in its message is exactly the case the
		// redaction layer exists for.
		_, _ = fmt.Fprintf(w, `{"code":400,"message":"invalid token Bearer %s"}`, secret)
	}))
	defer server.Close()

	ex := testExecutor()
	inner := ex.DecodeError
	ex.DecodeError = func(status int, body []byte) (string, string) {
		code, message := inner(status, body)
		return code, RedactString(message)
	}

	client := testClient(t, server.URL, RetryPolicy{})
	_, err := Execute[synthetic](context.Background(), ex, "Describe", RetryThrottleOnly, invokerFor(t, client, server.URL))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaked the credential: %v", err)
	}
}

func TestExecuteNoContentDiscardsTheBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"result":{"ok":true}}`)
	}))
	defer server.Close()

	client := testClient(t, server.URL, RetryPolicy{})
	if err := ExecuteNoContent(context.Background(), testExecutor(), "Delete", RetryServerErrors, invokerFor(t, client, server.URL)); err != nil {
		t.Fatalf("ExecuteNoContent: %v", err)
	}
}

func TestExecuteRawReturnsTheUndecodedBody(t *testing.T) {
	const body = `{"result":"pending"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	client := testClient(t, server.URL, RetryPolicy{})
	got, err := ExecuteRaw(context.Background(), testExecutor(), "Destroy", RetryServerErrors, invokerFor(t, client, server.URL))
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if string(got) != body {
		t.Errorf("got %q, want %q", got, body)
	}
}

// Execute passes the operation's declared retry safety down to the transport. Without this the
// per-operation idempotency decision would be silently ignored.
func TestExecutePropagatesRetrySafety(t *testing.T) {
	var seen RetrySafety = -1
	invoke := func(ctx context.Context) (*http.Response, error) {
		seen = retrySafetyFrom(ctx)
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	}
	if _, err := Execute[synthetic](context.Background(), testExecutor(), "Describe", RetryServerErrors, invoke); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen != RetryServerErrors {
		t.Errorf("retry safety on the request context = %v, want RetryServerErrors", seen)
	}
}

func TestExecuteRequiresAnErrorDecoder(t *testing.T) {
	invoke := func(context.Context) (*http.Response, error) {
		t.Fatal("the request should not have been sent")
		return nil, nil
	}
	if _, err := Execute[synthetic](context.Background(), &Executor{Service: ServiceCloud}, "Describe", RetryThrottleOnly, invoke); err == nil {
		t.Fatal("expected a misconfiguration error")
	}
	if _, err := Execute[synthetic](context.Background(), nil, "Describe", RetryThrottleOnly, invoke); err == nil {
		t.Fatal("expected a misconfiguration error for a nil executor")
	}
}

func TestRequestIDPrefersTheMostSpecificHeader(t *testing.T) {
	header := http.Header{}
	header.Set("X-Request-Id", "generic")
	header.Set("X-Amzn-Requestid", "specific")
	if got := requestID(header); got != "specific" {
		t.Errorf("requestID = %q, want %q", got, "specific")
	}
	if got := requestID(http.Header{}); got != "" {
		t.Errorf("requestID = %q, want empty", got)
	}
}
