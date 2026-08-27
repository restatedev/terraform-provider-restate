package api

import (
	"context"
	"errors"
	"testing"
	"time"
)

// instantPoll runs the loop without real waiting, and records the observed intervals.
func instantPoll(interval time.Duration, waits *int) PollConfig {
	return PollConfig{
		Interval: interval,
		sleep: func(ctx context.Context, d time.Duration) error {
			if waits != nil {
				*waits++
			}
			return ctx.Err()
		},
	}
}

func TestPollUntilChecksBeforeWaiting(t *testing.T) {
	waits := 0
	fetches := 0

	got, err := PollUntil(context.Background(), instantPoll(time.Second, &waits),
		func(context.Context) (string, error) {
			fetches++
			return "ACTIVE", nil
		},
		func(status string) (bool, error) { return status == "ACTIVE", nil },
	)
	if err != nil {
		t.Fatalf("PollUntil: %v", err)
	}
	if got != "ACTIVE" {
		t.Errorf("got %q, want ACTIVE", got)
	}
	if fetches != 1 {
		t.Errorf("fetches = %d, want 1", fetches)
	}
	// An operation that is already complete must not be delayed by a pointless first wait.
	if waits != 0 {
		t.Errorf("waits = %d, want 0", waits)
	}
}

func TestPollUntilLoopsUntilTerminal(t *testing.T) {
	statuses := []string{"PENDING", "PENDING", "ACTIVE"}
	index := 0
	waits := 0

	got, err := PollUntil(context.Background(), instantPoll(time.Second, &waits),
		func(context.Context) (string, error) {
			status := statuses[index]
			index++
			return status, nil
		},
		func(status string) (bool, error) { return status == "ACTIVE", nil },
	)
	if err != nil {
		t.Fatalf("PollUntil: %v", err)
	}
	if got != "ACTIVE" {
		t.Errorf("got %q, want ACTIVE", got)
	}
	if waits != 2 {
		t.Errorf("waits = %d, want 2", waits)
	}
}

// A poll loop that swallows fetch errors turns an authentication failure into a timeout, which is
// the least actionable diagnostic possible.
func TestPollUntilAbortsOnAFetchError(t *testing.T) {
	sentinel := errors.New("403 forbidden")
	fetches := 0

	_, err := PollUntil(context.Background(), instantPoll(time.Second, nil),
		func(context.Context) (string, error) {
			fetches++
			return "", sentinel
		},
		func(string) (bool, error) { return false, nil },
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the fetch error", err)
	}
	if fetches != 1 {
		t.Errorf("fetches = %d, want 1", fetches)
	}
}

// A terminal failure state must fail immediately rather than burn the whole resource timeout.
func TestPollUntilAbortsOnATerminalFailureState(t *testing.T) {
	sentinel := errors.New("environment provisioning FAILED")

	got, err := PollUntil(context.Background(), instantPoll(time.Second, nil),
		func(context.Context) (string, error) { return "FAILED", nil },
		func(status string) (bool, error) {
			if status == "FAILED" {
				return false, sentinel
			}
			return status == "ACTIVE", nil
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the terminal-state error", err)
	}
	// The last observed state is returned alongside the error so the caller can report it.
	if got != "FAILED" {
		t.Errorf("observed state = %q, want FAILED", got)
	}
}

func TestPollUntilTimesOut(t *testing.T) {
	cfg := PollConfig{Interval: time.Millisecond, Timeout: 20 * time.Millisecond}

	got, err := PollUntil(context.Background(), cfg,
		func(context.Context) (string, error) { return "PENDING", nil },
		func(string) (bool, error) { return false, nil },
	)
	if !errors.Is(err, ErrPollTimeout) {
		t.Fatalf("err = %v, want ErrPollTimeout", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline cause preserved", err)
	}
	// The caller needs the last observed state to explain what it gave up waiting for.
	if got != "PENDING" {
		t.Errorf("observed state = %q, want PENDING", got)
	}
}

// A Terraform resource timeout reaches this code as a context deadline, so the loop must respect
// one even with no Timeout of its own.
func TestPollUntilRespectsTheCallersDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := PollUntil(ctx, PollConfig{Interval: time.Millisecond},
		func(context.Context) (string, error) { return "PENDING", nil },
		func(string) (bool, error) { return false, nil },
	)
	if !errors.Is(err, ErrPollTimeout) {
		t.Fatalf("err = %v, want ErrPollTimeout", err)
	}
}

func TestPollUntilPassesTheBoundedContextToFetch(t *testing.T) {
	cfg := PollConfig{Interval: time.Millisecond, Timeout: time.Minute}

	_, err := PollUntil(context.Background(), cfg,
		func(ctx context.Context) (string, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("fetch received a context with no deadline")
			} else if time.Until(deadline) > time.Minute {
				t.Errorf("fetch deadline is %v away, want at most the poll timeout", time.Until(deadline))
			}
			return "ACTIVE", nil
		},
		func(string) (bool, error) { return true, nil },
	)
	if err != nil {
		t.Fatalf("PollUntil: %v", err)
	}
}

func TestPollUntilUsesADefaultIntervalWhenUnset(t *testing.T) {
	// An unset interval must not become a zero-delay busy loop.
	if got := (PollConfig{}).Interval; got != 0 {
		t.Fatalf("precondition: zero PollConfig has interval %v", got)
	}
	fetches := 0
	cfg := PollConfig{sleep: func(ctx context.Context, d time.Duration) error {
		if d != DefaultPollConfig().Interval {
			t.Errorf("wait = %v, want the default interval %v", d, DefaultPollConfig().Interval)
		}
		return errors.New("stop")
	}}
	_, err := PollUntil(context.Background(), cfg,
		func(context.Context) (string, error) { fetches++; return "PENDING", nil },
		func(string) (bool, error) { return false, nil },
	)
	if err == nil {
		t.Fatal("expected the loop to stop")
	}
	if fetches != 1 {
		t.Errorf("fetches = %d, want 1", fetches)
	}
}

// A deadline can expire while a fetch is in flight rather than during a wait. Both are the loop
// running out of time, so both must be reported as a timeout; otherwise the caller's timeout
// handling would work only when the timing happened to land between polls.
func TestPollUntilReportsATimeoutThatLandsDuringAFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	_, err := PollUntil(ctx, PollConfig{Interval: time.Hour},
		func(ctx context.Context) (string, error) {
			cancel()
			return "", ctx.Err()
		},
		func(string) (bool, error) { return false, nil },
	)
	if !errors.Is(err, ErrPollTimeout) {
		t.Fatalf("err = %v, want ErrPollTimeout", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the underlying cause preserved", err)
	}
}

// A fetch error that is not caused by the deadline must not be relabelled as a timeout, or an
// expired credential becomes indistinguishable from a slow provision.
func TestPollUntilDoesNotRelabelAnUnrelatedFetchError(t *testing.T) {
	sentinel := errors.New("401 unauthorized")

	_, err := PollUntil(context.Background(), instantPoll(time.Second, nil),
		func(context.Context) (string, error) { return "", sentinel },
		func(string) (bool, error) { return false, nil },
	)
	if errors.Is(err, ErrPollTimeout) {
		t.Errorf("err = %v, want the fetch error unlabelled", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the fetch error", err)
	}
}

// A single hung fetch must cost one attempt, not the whole budget: without a per-attempt bound, a
// thirty-minute provisioning timeout could be spent on one describe that never answers. The loop
// therefore presses on past a hung attempt, because a slow server is not a failed operation.
func TestPollUntilBoundsEachAttempt(t *testing.T) {
	cfg := PollConfig{Interval: time.Millisecond, AttemptTimeout: 20 * time.Millisecond}
	attempts := 0

	statuses := []string{"HUNG", "HUNG", "ACTIVE"}
	got, err := PollUntil(context.Background(), cfg,
		func(ctx context.Context) (string, error) {
			status := statuses[attempts]
			attempts++
			if status == "HUNG" {
				// Never answers; only the attempt deadline ends this.
				<-ctx.Done()
				return "", ctx.Err()
			}
			return status, nil
		},
		func(status string) (bool, error) { return status == "ACTIVE", nil },
	)
	if err != nil {
		t.Fatalf("PollUntil: %v", err)
	}
	if got != "ACTIVE" {
		t.Errorf("got = %q, want ACTIVE", got)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3: each hung fetch should cost one attempt", attempts)
	}
}

// Tolerating a hung attempt must not become tolerating an endpoint that never answers, including when
// the caller set no deadline of its own for the loop to run out of.
func TestPollUntilGivesUpOnPersistentlyHungAttempts(t *testing.T) {
	cfg := PollConfig{Interval: time.Millisecond, AttemptTimeout: 5 * time.Millisecond}
	attempts := 0

	_, err := PollUntil(context.Background(), cfg,
		func(ctx context.Context) (string, error) {
			attempts++
			<-ctx.Done()
			return "", ctx.Err()
		},
		func(string) (bool, error) { return true, nil },
	)
	if !errors.Is(err, ErrPollTimeout) {
		t.Fatalf("err = %v, want ErrPollTimeout", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the attempt deadline to stay reachable", err)
	}
	if attempts != maxConsecutiveAttemptTimeouts {
		t.Errorf("attempts = %d, want %d", attempts, maxConsecutiveAttemptTimeouts)
	}
}

// A fetch that fails for a reason other than running out of time is still fatal on its first
// occurrence: retrying an expired credential would report it as a timeout.
func TestPollUntilDoesNotRetryANonTimeoutFetchError(t *testing.T) {
	cfg := PollConfig{Interval: time.Millisecond, AttemptTimeout: time.Minute}
	attempts := 0
	unauthorized := errors.New("401 unauthorized")

	_, err := PollUntil(context.Background(), cfg,
		func(context.Context) (string, error) {
			attempts++
			return "", unauthorized
		},
		func(string) (bool, error) { return true, nil },
	)
	if !errors.Is(err, unauthorized) {
		t.Fatalf("err = %v, want the fetch error", err)
	}
	if errors.Is(err, ErrPollTimeout) {
		t.Errorf("err = %v, must not be reported as a timeout", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

// The per-attempt bound must not shorten the caller's own deadline when that is the tighter of the
// two, and must not extend past it either.
func TestPollUntilAttemptBoundRespectsTheCallersDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := PollUntil(ctx, PollConfig{Interval: time.Millisecond, AttemptTimeout: time.Hour},
		func(ctx context.Context) (string, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("fetch received a context with no deadline")
			} else if time.Until(deadline) > time.Minute {
				t.Errorf("fetch deadline is %v away, want the caller's tighter one", time.Until(deadline))
			}
			return "PENDING", nil
		},
		func(string) (bool, error) { return false, nil },
	)
	if !errors.Is(err, ErrPollTimeout) {
		t.Fatalf("err = %v, want ErrPollTimeout", err)
	}
}
