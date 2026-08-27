package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// The capability interfaces below describe what an endpoint can do. Each facade adapter implements
// exactly the ones its endpoint supports and asserts as much at compile time, so an endpoint's
// missing capability is a build error rather than a runtime surprise. Subscriptions and Cloud API
// keys, for instance, deliberately have no Updater.
//
// The type parameters keep identifiers, inputs, and remote models distinct, so a subscription ID
// cannot be passed to a deployment operation and a create request cannot be reused as an update
// request.

// Getter reads one object by identity.
type Getter[ID comparable, Remote any] interface {
	Get(ctx context.Context, id ID) (Remote, error)
}

// Creator creates one object and returns whatever the API reports about it.
type Creator[Input any, Remote any] interface {
	Create(ctx context.Context, input Input) (Remote, error)
}

// Updater modifies an existing object in place.
type Updater[ID comparable, Input any, Remote any] interface {
	Update(ctx context.Context, id ID, input Input) (Remote, error)
}

// Deleter removes an object. Options is endpoint-specific and is where a destructive escape hatch
// such as forced deletion lives, so no caller can request one implicitly.
type Deleter[ID comparable, Options any] interface {
	Delete(ctx context.Context, id ID, options Options) error
}

// Lister enumerates every object of a kind.
//
// Neither pinned contract paginates any list operation, so this returns a complete slice. If a
// future snapshot introduces pagination, this interface is where it gets absorbed — callers must
// not assume one request per call.
type Lister[Remote any] interface {
	List(ctx context.Context) ([]Remote, error)
}

// Invoker performs one HTTP request. The generated clients own request construction, so the
// generic helpers take a closure over a generated method rather than building requests themselves.
type Invoker func(ctx context.Context) (*http.Response, error)

// ErrorDecoder extracts an API's own error representation from a response body. The two contracts
// disagree on the shape — the Cloud API sends `{code, message}` and the Admin API sends
// `{message, restate_code}` — so each facade supplies its own.
//
// It must tolerate an empty or malformed body and never return the raw body, which may contain
// credentials echoed back or user payloads.
type ErrorDecoder func(status int, body []byte) (code, message string)

// Executor holds the per-API decisions the generic helpers need: which service they are talking to,
// how to read its errors, and how much of a response to trust.
type Executor struct {
	// Service names the API, for diagnostics.
	Service Service
	// DecodeError reads the API's error body. Required.
	DecodeError ErrorDecoder
	// MaxResponseBytes bounds how much of a response body is read. Zero uses
	// DefaultMaxResponseBytes. A discovery response listing every handler of every service can be
	// large, so this is generous but finite.
	MaxResponseBytes int64
}

// DefaultMaxResponseBytes bounds a response body. It is well above any response either contract can
// produce for the operations this provider calls, while still preventing an unbounded read.
const DefaultMaxResponseBytes int64 = 32 << 20

// Execute performs one request and decodes a JSON success body into Out.
//
// safety states how much of a failure the operation may be retried through and is not optional:
// the caller is the only party that knows whether repeating this particular request could create a
// second object.
func Execute[Out any](ctx context.Context, ex *Executor, operation string, safety RetrySafety, invoke Invoker) (Out, error) {
	var zero Out

	body, err := executeRaw(ctx, ex, operation, safety, invoke)
	if err != nil {
		return zero, err
	}

	// An operation whose contract declares no response body legitimately returns nothing. Decoding
	// an empty body into a struct would fail, so treat it as the zero value.
	if len(body) == 0 {
		return zero, nil
	}

	var out Out
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, &Error{
			Service:   ex.Service,
			Operation: operation,
			Message:   "the API returned a response body that could not be decoded",
			Err:       err,
		}
	}
	return out, nil
}

// ExecuteNoContent performs one request and discards its body. Use it for operations whose response
// carries no information the provider acts on.
func ExecuteNoContent(ctx context.Context, ex *Executor, operation string, safety RetrySafety, invoke Invoker) error {
	_, err := executeRaw(ctx, ex, operation, safety, invoke)
	return err
}

// ExecuteRaw performs one request and returns its undecoded success body. Use it for an operation
// whose response is not JSON, or whose union shape the facade decodes itself.
func ExecuteRaw(ctx context.Context, ex *Executor, operation string, safety RetrySafety, invoke Invoker) ([]byte, error) {
	return executeRaw(ctx, ex, operation, safety, invoke)
}

func executeRaw(ctx context.Context, ex *Executor, operation string, safety RetrySafety, invoke Invoker) ([]byte, error) {
	if ex == nil || ex.DecodeError == nil {
		return nil, errors.New("api: executor is missing an error decoder")
	}

	resp, err := invoke(WithRetrySafety(ctx, safety))
	if err != nil {
		// Prefer the caller's own cancellation reason: a Terraform operation that timed out should
		// say so, not report a generic connection failure.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, &Error{
				Service:   ex.Service,
				Operation: operation,
				Message:   "the request was cancelled before it completed",
				Err:       ctxErr,
			}
		}
		return nil, &Error{
			Service:   ex.Service,
			Operation: operation,
			Message:   "could not reach the API",
			Retryable: isRetryableTransportError(err),
			Err:       redactError(err),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	limit := ex.MaxResponseBytes
	if limit <= 0 {
		limit = DefaultMaxResponseBytes
	}

	// Read one byte past the limit so an oversized body is reported rather than silently truncated
	// into a decoding error.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if int64(len(body)) > limit {
		return nil, &Error{
			Service:    ex.Service,
			Operation:  operation,
			StatusCode: resp.StatusCode,
			RequestID:  requestID(resp.Header),
			Message:    fmt.Sprintf("the API returned a response larger than the %d byte limit", limit),
		}
	}
	if readErr != nil {
		return nil, &Error{
			Service:    ex.Service,
			Operation:  operation,
			StatusCode: resp.StatusCode,
			RequestID:  requestID(resp.Header),
			Message:    "the API response could not be read",
			Retryable:  true,
			Err:        redactError(readErr),
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		code, message := ex.DecodeError(resp.StatusCode, body)
		return nil, &Error{
			Service:    ex.Service,
			Operation:  operation,
			StatusCode: resp.StatusCode,
			Code:       code,
			Message:    message,
			RequestID:  requestID(resp.Header),
			Retryable:  retryableStatus(resp.StatusCode, resp.Header, safety),
		}
	}

	return body, nil
}

// requestIDHeaders are the correlation headers either API might send, most specific first. Keeping
// the request ID in an error is the difference between an actionable support request and a
// screenshot.
var requestIDHeaders = []string{
	"X-Amzn-Requestid",
	"X-Amzn-Trace-Id",
	"X-Request-Id",
	"X-Correlation-Id",
	"X-Amz-Cf-Id",
}

func requestID(header http.Header) string {
	for _, name := range requestIDHeaders {
		if value := header.Get(name); value != "" {
			return value
		}
	}
	return ""
}
