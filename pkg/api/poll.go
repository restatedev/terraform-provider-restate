package api

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PollConfig bounds a convergence loop. Cloud environment creation and destruction are
// asynchronous: the mutating call returns immediately and the caller must watch the environment's
// status until it reaches a terminal state.
type PollConfig struct {
	// Interval is the wait between polls.
	Interval time.Duration
	// Timeout bounds the whole loop. Zero relies on the caller's context deadline, which is how a
	// Terraform resource timeout reaches this code.
	Timeout time.Duration

	// sleep waits, honouring ctx. Tests substitute an instant implementation.
	sleep func(ctx context.Context, d time.Duration) error
}

// DefaultPollConfig polls often enough that a fast operation is not needlessly slowed, without
// hammering a rate-limited API during a slow one.
func DefaultPollConfig() PollConfig {
	return PollConfig{Interval: 5 * time.Second}
}

// ErrPollTimeout is returned when a poll loop runs out of time. Callers wrap it with the object's
// identifier so an operator can find and clean up whatever was left half-provisioned.
var ErrPollTimeout = errors.New("timed out waiting for the operation to reach a terminal state")

// PollUntil repeatedly calls fetch until done reports that the observed state is terminal.
//
// fetch is called immediately, before the first wait, because an operation may already be complete.
// A fetch error aborts the loop: a poll loop that swallows errors turns an authentication failure
// into a timeout. Callers that need to tolerate a specific error — a not-found response while
// waiting for a delete to converge, say — handle it inside fetch.
//
// done returning an error aborts the loop, which is how a terminal failure state such as an
// environment reporting FAILED is reported without waiting for the timeout.
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

	for attempt := 1; ; attempt++ {
		observed, err := fetch(ctx)
		if err != nil {
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
