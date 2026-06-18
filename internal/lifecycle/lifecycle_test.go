package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"hermex/internal/lifecycle"
)

// recorder collects ordered event labels from concurrent goroutines.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// TestRunStopsOnContextCancel proves the graceful path: when the context is
// cancelled, every component is shut down, the cleanups run strictly after every
// Shutdown returns, and Run reports success (a component's post-shutdown Start
// return is not treated as a failure).
func TestRunStopsOnContextCancel(t *testing.T) {
	rec := &recorder{}
	mkComp := func(name string) lifecycle.Component {
		stop := make(chan struct{})
		return lifecycle.Func{
			StartFn: func() error { <-stop; return nil },
			ShutdownFn: func(context.Context) error {
				rec.add("shutdown:" + name)
				close(stop)
				return nil
			},
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // request shutdown immediately so the path is deterministic

	cleanup := func() error { rec.add("cleanup"); return nil }
	err := lifecycle.Run(ctx, time.Second, []lifecycle.Component{mkComp("a"), mkComp("b")}, cleanup)
	if err != nil {
		t.Fatalf("Run = %v, want nil on graceful shutdown", err)
	}

	ev := rec.snapshot()
	if len(ev) != 3 {
		t.Fatalf("events = %v, want two shutdowns + one cleanup", ev)
	}
	if ev[len(ev)-1] != "cleanup" {
		t.Errorf("cleanup ran at %v, want strictly after both shutdowns: %v", ev[len(ev)-1], ev)
	}
	if !slices.Contains(ev, "shutdown:a") || !slices.Contains(ev, "shutdown:b") {
		t.Errorf("not every component was shut down: %v", ev)
	}
}

// TestRunReportsStartFailure proves that a component whose Start returns while the
// context is still live is a genuine failure: Run shuts the other components down
// and surfaces that error. The context is never cancelled, so the failure is the
// only thing that can trigger shutdown.
func TestRunReportsStartFailure(t *testing.T) {
	wantErr := errors.New("listener bind failed")
	stop := make(chan struct{})
	shutdownCalled := make(chan struct{}, 1)

	failing := lifecycle.Func{
		StartFn:    func() error { return wantErr },
		ShutdownFn: func(context.Context) error { return nil },
	}
	healthy := lifecycle.Func{
		StartFn: func() error { <-stop; return nil },
		ShutdownFn: func(context.Context) error {
			close(stop)
			shutdownCalled <- struct{}{}
			return nil
		},
	}

	err := lifecycle.Run(context.Background(), time.Second, []lifecycle.Component{failing, healthy})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run = %v, want the genuine Start failure %v", err, wantErr)
	}
	select {
	case <-shutdownCalled:
	default:
		t.Error("the healthy component was not shut down after the other failed")
	}
}

// TestRunHonorsShutdownTimeout proves the single shared deadline is plumbed into
// every component's Shutdown: a component that drains until the deadline fires
// returns the deadline error, and Run returns shortly after the timeout rather
// than blocking forever.
func TestRunHonorsShutdownTimeout(t *testing.T) {
	stop := make(chan struct{})
	slow := lifecycle.Func{
		StartFn: func() error { <-stop; return nil },
		ShutdownFn: func(ctx context.Context) error {
			<-ctx.Done() // never finishes on its own; only the deadline releases it
			close(stop)
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	const timeout = 50 * time.Millisecond
	start := time.Now()
	err := lifecycle.Run(ctx, timeout, []lifecycle.Component{slow})
	elapsed := time.Since(start)

	if elapsed < timeout {
		t.Errorf("Run returned in %v, before the %v shutdown deadline", elapsed, timeout)
	}
	if elapsed > timeout+2*time.Second {
		t.Errorf("Run took %v, far past the %v deadline — the shared timeout was not honored", elapsed, timeout)
	}
	if err == nil {
		t.Error("Run = nil, want the shutdown-deadline error surfaced")
	}
}
