package api

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PollConfig bounds a convergence loop. Cloud environment creation and destruction are asynchronous:
// the mutating call returns immediately, and the caller watches the status until it is terminal.
type PollConfig struct {
	// Interval is the wait between polls.
	Interval time.Duration
	// Timeout bounds the whole loop. Zero relies on the caller's context deadline, which is how a
	// Terraform resource timeout reaches here.
	Timeout time.Duration
	// AttemptTimeout bounds one fetch. Without it a single hung request would consume the whole
	// budget -- a thirty-minute provisioning timeout spent on one describe that never answers. Zero
	// uses DefaultPollConfig's value.
	//
	// An attempt that runs out of time costs that attempt rather than the whole loop: see
	// maxConsecutiveAttemptTimeouts.
	AttemptTimeout time.Duration

	// sleep waits, honouring ctx. Tests substitute an instant implementation.
	sleep func(ctx context.Context, d time.Duration) error
}

// DefaultPollConfig polls often enough not to slow a fast operation, without hammering a
// rate-limited API during a slow one.
func DefaultPollConfig() PollConfig {
	return PollConfig{Interval: 5 * time.Second, AttemptTimeout: 30 * time.Second}
}

// ErrPollTimeout is returned when a poll loop runs out of time. Callers wrap it with the object's
// identifier, so an operator can find whatever was left half-provisioned.
var ErrPollTimeout = errors.New("timed out waiting for the operation to reach a terminal state")

// maxConsecutiveAttemptTimeouts is how many fetches in a row may exhaust AttemptTimeout before the
// loop gives up.
//
// A fetch that runs out of time is a slow server, not a failed operation, so it must not end a
// convergence loop: an environment create that polls for thirty minutes should survive one describe
// that hangs. But a permanently unresponsive endpoint has to fail even when the caller set no
// deadline, so the tolerance is bounded rather than unlimited. A fetch that fails for any other
// reason -- an expired credential, a malformed response -- is still fatal on the first occurrence.
const maxConsecutiveAttemptTimeouts = 3

// PollUntil repeatedly calls fetch until done reports the observed state terminal.
//
// fetch runs immediately, before the first wait, because the operation may already be complete. A
// fetch error aborts the loop: swallowing errors would turn an authentication failure into a timeout.
// A caller that needs to tolerate a specific error -- a not-found while waiting for a delete to
// converge, say -- handles it inside fetch.
//
// The one exception is a fetch that exhausted AttemptTimeout, which says nothing about the operation
// and is retried up to maxConsecutiveAttemptTimeouts times.
//
// done returning an error also aborts, which is how a terminal failure such as FAILED is reported
// without waiting out the timeout.
func PollUntil[T any](ctx context.Context, cfg PollConfig, fetch func(context.Context) (T, error), done func(T) (bool, error)) (T, error) {
	var zero T

	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultPollConfig().Interval
	}

	attemptTimeout := cfg.AttemptTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = DefaultPollConfig().AttemptTimeout
	}

	timedOutAttempts := 0

	for attempt := 1; ; attempt++ {
		observed, expired, err := fetchOnce(ctx, attemptTimeout, fetch)
		if err != nil {
			// A deadline that expires while a fetch is in flight is still the loop running out of
			// time, not a distinct failure. Report it as a timeout so callers have one condition to
			// check regardless of which phase the deadline happened to land in, while keeping the
			// underlying error reachable through errors.Is.
			if ctx.Err() != nil {
				return zero, fmt.Errorf("%w after %d attempts: %w", ErrPollTimeout, attempt, err)
			}
			if !expired {
				return zero, err
			}

			timedOutAttempts++
			if timedOutAttempts >= maxConsecutiveAttemptTimeouts {
				return zero, fmt.Errorf("%w: %d consecutive attempts exceeded the %s per-attempt timeout: %w",
					ErrPollTimeout, timedOutAttempts, attemptTimeout, err)
			}
			if waitErr := cfg.wait(ctx, interval); waitErr != nil {
				return zero, fmt.Errorf("%w after %d attempts: %w", ErrPollTimeout, attempt, err)
			}
			continue
		}
		timedOutAttempts = 0

		finished, err := done(observed)
		if err != nil {
			return observed, err
		}
		if finished {
			return observed, nil
		}

		if err := cfg.wait(ctx, interval); err != nil {
			return observed, fmt.Errorf("%w after %d attempts: %w", ErrPollTimeout, attempt, err)
		}
	}
}

// fetchOnce bounds one fetch, so a hung request costs an attempt rather than the whole loop.
//
// expired distinguishes "this attempt ran out of time" from "this operation failed", which the caller
// needs because only the former may be retried. It is reported only when the attempt's own deadline
// expired and the caller's context is still live, so the loop never mistakes the caller giving up for
// a slow server.
func fetchOnce[T any](ctx context.Context, timeout time.Duration, fetch func(context.Context) (T, error)) (observed T, expired bool, err error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	observed, err = fetch(attemptCtx)
	if err != nil && ctx.Err() == nil && attemptCtx.Err() != nil {
		return observed, true, err
	}
	return observed, false, err
}

func (c PollConfig) wait(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
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
