package lifecycle

import (
	"context"
	"fmt"
)

// loop is a background loop whose Shutdown joins the loop goroutine.
type loop struct {
	fn     func(context.Context)
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// Loop returns a Component that runs fn as a background loop and whose Shutdown
// blocks until fn has returned. A bare cancel is not enough: Run launches every
// Start on its own goroutine and runs the cleanups as soon as the Shutdowns
// return, so a loop that has only been signalled is still touching the resources
// (the outbound spool, the directory database) that the cleanups are about to
// close. The join is what makes the package's drain-then-close promise hold for a
// loop rather than only for a connection server.
//
// fn must return when its context is cancelled. Shutdown gives up at its own
// deadline and reports an error rather than stalling the daemon, so a loop whose
// pass can run long must watch for cancellation inside the pass, not only between
// passes; otherwise the deadline expires and the close races the pass after all.
func Loop(fn func(context.Context)) Component {
	ctx, cancel := context.WithCancel(context.Background())
	return &loop{fn: fn, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// Start runs the loop until Shutdown cancels it.
func (l *loop) Start() error {
	defer close(l.done)
	l.fn(l.ctx)
	return nil
}

// Shutdown cancels the loop and waits for it to return, up to ctx's deadline.
func (l *loop) Shutdown(ctx context.Context) error {
	l.cancel()
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("background loop did not drain before the shutdown deadline: %w", ctx.Err())
	}
}
