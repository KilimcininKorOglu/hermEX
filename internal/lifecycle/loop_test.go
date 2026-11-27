package lifecycle_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hermex/internal/lifecycle"
)

// TestLoopCleanupWaitsForTheLoop is the regression this component exists for: the
// MTA closes its outbound spool as a cleanup, and a loop that had only been
// signalled would still be mid-delivery when that close ran. The cleanup must not
// run until the loop has actually returned.
func TestLoopCleanupWaitsForTheLoop(t *testing.T) {
	var drained, closedWhileRunning atomic.Bool
	started := make(chan struct{})

	comp := lifecycle.Loop(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		// Cancellation arrives while the pass is still settling work. The sleep is
		// what makes the assertion decide something: without the join the cleanup
		// runs during it, and drained is still false.
		time.Sleep(50 * time.Millisecond)
		drained.Store(true)
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	err := lifecycle.Run(ctx, time.Second, []lifecycle.Component{comp}, func() error {
		if !drained.Load() {
			closedWhileRunning.Store(true)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if closedWhileRunning.Load() {
		t.Error("the cleanup ran while the loop was still working; the spool would have closed under an in-flight delivery")
	}
}

// TestLoopShutdownReportsAStuckLoop proves a loop that ignores cancellation does
// not stall the whole daemon: Shutdown gives up at its deadline and reports it.
// Silently hanging would turn a slow drain into a hung process.
func TestLoopShutdownReportsAStuckLoop(t *testing.T) {
	release := make(chan struct{})
	startErr := make(chan error, 1)

	comp := lifecycle.Loop(func(context.Context) { <-release })
	go func() { startErr <- comp.Start() }()
	t.Cleanup(func() {
		if err := <-startErr; err != nil {
			t.Errorf("Start = %v, want nil", err)
		}
	})
	// Register after startErr check so LIFO releases the loop before waiting for Start.
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := comp.Shutdown(ctx)
	if err == nil {
		t.Fatal("Shutdown reported success for a loop that never returned")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), "drain") {
		t.Errorf("error = %v, want it to name the failed drain", err)
	}
}

// TestLoopShutdownIsPromptWhenTheLoopReturns proves the join adds no delay of its
// own: a loop that honours cancellation is shut down immediately.
func TestLoopShutdownIsPromptWhenTheLoopReturns(t *testing.T) {
	startErr := make(chan error, 1)
	comp := lifecycle.Loop(func(ctx context.Context) { <-ctx.Done() })
	go func() { startErr <- comp.Start() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := comp.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-startErr; err != nil {
		t.Errorf("Start = %v, want nil after Shutdown", err)
	}
}

// TestLoopShutdownCanceledContext proves an already-cancelled shutdown context
// returns context.Canceled while still cancelling the loop's own context.
func TestLoopShutdownCanceledContext(t *testing.T) {
	loopCanceled := make(chan struct{}, 1)
	release := make(chan struct{})
	startErr := make(chan error, 1)

	comp := lifecycle.Loop(func(ctx context.Context) {
		<-ctx.Done()
		loopCanceled <- struct{}{}
		<-release
	})
	go func() { startErr <- comp.Start() }()
	t.Cleanup(func() {
		if err := <-startErr; err != nil {
			t.Errorf("Start = %v, want nil", err)
		}
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := comp.Shutdown(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown = %v, want context.Canceled", err)
	}
	select {
	case <-loopCanceled:
	case <-time.After(time.Second):
		t.Error("Shutdown with canceled context did not cancel the loop context")
	}
}
