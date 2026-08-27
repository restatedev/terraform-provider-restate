package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Doer is the subset of *http.Client the generated clients need.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// TransportConfig describes one authenticated connection to one API.
type TransportConfig struct {
	// BaseURL is the API root, normalized by NormalizeBaseURL.
	BaseURL string
	// BearerToken authenticates every request. It is sent to this base URL and nowhere else.
	BearerToken string
	// UserAgent identifies the provider and Terraform versions.
	UserAgent string
	// Retry bounds retrying. The zero value disables it.
	Retry RetryPolicy
	// AllowInsecureHTTP permits a plaintext base URL for a non-loopback host, for testing against a
	// local or self-hosted Restate.
	AllowInsecureHTTP bool

	// Base is the underlying round tripper. Nil means http.DefaultTransport.
	Base http.RoundTripper
}

// NewTransport validates cfg and returns a client that authenticates every request and retries
// according to each request's declared retry safety.
func NewTransport(cfg TransportConfig) (*http.Client, error) {
	normalized, err := NormalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	if err := requireSecureScheme(parsed, cfg.AllowInsecureHTTP); err != nil {
		return nil, err
	}
	if cfg.BearerToken == "" {
		return nil, errors.New("a bearer token is required")
	}

	base := cfg.Base
	if base == nil {
		base = http.DefaultTransport
	}

	// No http.Client.Timeout: it is a hard cap on the whole request that would silently override a
	// longer context deadline, so a resource-level timeout -- deployment discovery's ten minutes, say --
	// could never take effect. Every request is bounded by a context instead; see Executor.RequestTimeout.
	return &http.Client{
		Transport: &authRoundTripper{
			token:     cfg.BearerToken,
			userAgent: cfg.UserAgent,
			next: &retryRoundTripper{
				policy: cfg.Retry,
				next:   base,
			},
		},
		// API endpoints are not expected to redirect. Refusing redirects keeps the bearer token scoped
		// to the configured endpoint: following one would pass the redirected request back through the
		// authentication transport, which would otherwise attach the token to the new destination.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// NormalizeBaseURL trims trailing slashes and whitespace, and rejects a URL that cannot address an
// API. It deliberately neither strips nor adds a path: "https://host/cloud" and "https://host"
// address different things, and the Cloud Management API's canonical URL includes the "/cloud" prefix.
func NormalizeBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("base URL is empty")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse base URL %q: %w", trimmed, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("base URL %q must use http or https, got scheme %q", trimmed, parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("base URL %q has no host", trimmed)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("base URL %q must not contain a query string or fragment", trimmed)
	}
	if parsed.User != nil {
		// Report the redacted form: the whole point of the rejection is that the URL carries a
		// credential, and quoting it back would copy that credential into logs and diagnostics.
		return "", fmt.Errorf("base URL %q must not contain credentials; pass the API key separately", parsed.Redacted())
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

// requireSecureScheme enforces HTTPS for anything that is not loopback: a bearer token over plaintext
// to a remote host is a credential leak.
func requireSecureScheme(parsed *url.URL, allowInsecure bool) error {
	if parsed.Scheme == "https" || allowInsecure {
		return nil
	}
	if isLoopbackHost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("base URL %q uses plaintext http for non-loopback host %q, which would send the API key unencrypted; use https, or set allow_insecure_http to override for a local or self-hosted server",
		parsed.Redacted(), parsed.Hostname())
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// authRoundTripper attaches the bearer token and user agent. It is outermost so the credential is
// added once and the retrying tripper below replays a fully formed request.
type authRoundTripper struct {
	token     string
	userAgent string
	next      http.RoundTripper
}

func (rt *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// RoundTrippers must not modify the request they are given.
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+rt.token)
	if rt.userAgent != "" {
		cloned.Header.Set("User-Agent", rt.userAgent)
	}
	return rt.next.RoundTrip(cloned)
}

// retryRoundTripper replays a request according to the retry safety on its context.
type retryRoundTripper struct {
	policy RetryPolicy
	next   http.RoundTripper
}

func (rt *retryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	safety := retrySafetyFrom(ctx)
	attempts := max(rt.policy.MaxAttempts, 1)

	// A request can only be replayed if its body can be rewound. Retrying a partially consumed body
	// would silently corrupt the request.
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		attempts = 1
	}

	var lastResponse *http.Response
	var lastErr error

	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if err := rt.rewind(req); err != nil {
				return nil, err
			}
		}

		lastResponse, lastErr = rt.next.RoundTrip(req)

		if lastErr != nil {
			if attempt == attempts || safety < RetryTransportErrors || !isRetryableTransportError(lastErr) {
				return nil, lastErr
			}
			if waitErr := rt.policy.wait(ctx, rt.policy.backoff(attempt)); waitErr != nil {
				return nil, lastErr
			}
			continue
		}

		if attempt == attempts || !retryableStatus(lastResponse.StatusCode, lastResponse.Header, safety) {
			return lastResponse, nil
		}

		delay := rt.policy.backoff(attempt)
		if serverDelay, ok := retryAfter(lastResponse.Header, time.Now()); ok {
			delay = min(serverDelay, rt.policy.maxDelay())
		}

		// Drain the discarded response, or the connection cannot be reused.
		drain(lastResponse)

		if err := rt.policy.wait(ctx, delay); err != nil {
			return nil, err
		}
	}

	return lastResponse, lastErr
}

func (p RetryPolicy) maxDelay() time.Duration {
	if p.MaxDelay > 0 {
		return p.MaxDelay
	}
	return DefaultRetryPolicy().MaxDelay
}

func (rt *retryRoundTripper) rewind(req *http.Request) error {
	if req.GetBody == nil {
		return nil
	}
	body, err := req.GetBody()
	if err != nil {
		return fmt.Errorf("rewind request body for retry: %w", err)
	}
	req.Body = body
	return nil
}

// isRetryableTransportError reports whether a failure to get any response is worth another try. A
// context cancellation is the caller's decision and is never retried.
func isRetryableTransportError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// A server closing an idle keep-alive connection mid-request surfaces as one of these rather than
	// as a net.Error.
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED)
}

func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}
