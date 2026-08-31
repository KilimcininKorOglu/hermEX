package lifecycle_test

import (
	"context"
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
	t.Cleanup(func() { close(release) })

	comp := lifecycle.Loop(func(context.Context) { <-release })
	go func() { _ = comp.Start() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := comp.Shutdown(ctx)
	if err == nil {
		t.Fatal("Shutdown reported success for a loop that never returned")
	}
	if !strings.Contains(err.Error(), "drain") {
		t.Errorf("error = %v, want it to name the failed drain", err)
	}
}

// TestLoopShutdownIsPromptWhenTheLoopReturns proves the join adds no delay of its
// own: a loop that honours cancellation is shut down immediately.
func TestLoopShutdownIsPromptWhenTheLoopReturns(t *testing.T) {
	comp := lifecycle.Loop(func(ctx context.Context) { <-ctx.Done() })
	go func() { _ = comp.Start() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := comp.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
