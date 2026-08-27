package api

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// RetrySafety says how much of a failure an operation may be retried through. Retry safety is a
// property of the operation, not of the transport, so every endpoint adapter states it explicitly.
// The zero value is the safest choice, so an adapter that forgets to declare it cannot
// accidentally duplicate a write.
type RetrySafety int

const (
	// RetryThrottleOnly retries only responses that prove the request was rejected without being
	// acted on: 429, and 503 with a Retry-After. Safe for non-idempotent creates.
	RetryThrottleOnly RetrySafety = iota

	// RetryServerErrors additionally retries retryable 5xx responses. Appropriate for operations
	// that are idempotent by construction — renames, tier changes, deletes — where repeating the
	// request cannot create a second object.
	RetryServerErrors

	// RetryTransportErrors additionally retries connection failures, where the provider cannot
	// tell whether the server saw the request at all. Only reads may use this.
	RetryTransportErrors
)

// retrySafetyKey carries the operation's retry safety to the retrying round tripper. It travels on
// the context because the generated clients own request construction.
type retrySafetyKey struct{}

// WithRetrySafety returns a context that permits the given amount of retrying.
func WithRetrySafety(ctx context.Context, safety RetrySafety) context.Context {
	return context.WithValue(ctx, retrySafetyKey{}, safety)
}

func retrySafetyFrom(ctx context.Context) RetrySafety {
	if safety, ok := ctx.Value(retrySafetyKey{}).(RetrySafety); ok {
		return safety
	}
	return RetryThrottleOnly
}

// RetryPolicy bounds retrying. A zero MaxAttempts disables retries entirely.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts, including the first.
	MaxAttempts int
	// BaseDelay is the first backoff interval; it doubles each attempt up to MaxDelay.
	BaseDelay time.Duration
	// MaxDelay caps the backoff interval.
	MaxDelay time.Duration

	// randFloat64 returns a value in [0, 1) for jitter. Tests substitute a deterministic function;
	// production leaves it nil and gets math/rand/v2.
	randFloat64 func() float64
	// sleep waits, honouring ctx. Tests substitute an instant implementation.
	sleep func(ctx context.Context, d time.Duration) error
}

// DefaultRetryPolicy is a conservative policy: a handful of attempts over a few seconds, short
// enough that a Terraform operation fails fast on a real outage rather than hanging until timeout.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 4, BaseDelay: 250 * time.Millisecond, MaxDelay: 5 * time.Second}
}

// backoff returns the wait before the given attempt number, which is 1-based and counts the
// attempt that just failed. Jitter is full-width: the delay is uniform over [0, exponential), which
// avoids synchronising retries across parallel Terraform resource operations.
func (p RetryPolicy) backoff(attempt int) time.Duration {
	base := p.BaseDelay
	if base <= 0 {
		base = DefaultRetryPolicy().BaseDelay
	}
	max := p.MaxDelay
	if max <= 0 {
		max = DefaultRetryPolicy().MaxDelay
	}

	delay := base
	for range attempt - 1 {
		delay *= 2
		if delay >= max {
			delay = max
			break
		}
	}

	random := p.randFloat64
	if random == nil {
		random = rand.Float64
	}
	return time.Duration(random() * float64(delay))
}

func (p RetryPolicy) wait(ctx context.Context, d time.Duration) error {
	if p.sleep != nil {
		return p.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryableStatus classifies a response status against what the operation permits.
//
// A 429 always means the request was throttled rather than acted on. A 503 with Retry-After is the
// documented form of "come back later" and is treated the same way. Other 5xx responses may mean
// the write partially happened, so they are retried only for operations that said so.
//
// 4xx responses other than 429 are never retried: a validation or authentication failure will not
// resolve itself, and retrying it just multiplies the failed audit-log entries.
func retryableStatus(status int, header http.Header, safety RetrySafety) bool {
	switch {
	case status == http.StatusTooManyRequests:
		return true
	case status == http.StatusServiceUnavailable && header.Get("Retry-After") != "":
		return true
	case status >= 500 && status <= 599:
		return safety >= RetryServerErrors
	default:
		return false
	}
}

// retryAfter reads a Retry-After header in either of its documented forms. It returns false when
// the header is absent or unparseable, leaving the caller on exponential backoff.
func retryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	value := header.Get("Retry-After")
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		if delay := at.Sub(now); delay > 0 {
			return delay, true
		}
		return 0, true
	}
	return 0, false
}
