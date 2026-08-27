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

// PollUntil repeatedly calls fetch until done reports the observed state terminal.
//
// fetch runs immediately, before the first wait, because the operation may already be complete. A
// fetch error aborts the loop: swallowing errors would turn an authentication failure into a timeout.
// A caller that needs to tolerate a specific error -- a not-found while waiting for a delete to
// converge, say -- handles it inside fetch.
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

	for attempt := 1; ; attempt++ {
		observed, err := fetchOnce(ctx, attemptTimeout, fetch)
		if err != nil {
			// A deadline that expires while a fetch is in flight is still the loop running out of
			// time, not a distinct failure. Report it as a timeout so callers have one condition to
			// check regardless of which phase the deadline happened to land in, while keeping the
			// underlying error reachable through errors.Is.
			if ctx.Err() != nil {
				return zero, fmt.Errorf("%w after %d attempts: %w", ErrPollTimeout, attempt, err)
			}
			return zero, err
		}

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
func fetchOnce[T any](ctx context.Context, timeout time.Duration, fetch func(context.Context) (T, error)) (T, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return fetch(attemptCtx)
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
