// Package lifecycle coordinates graceful startup and shutdown for the hermEX
// daemons. A daemon's main builds the set of long-running Components (HTTP
// servers, mail listeners, background workers) plus an ordered list of resource
// cleanups (database handles, object stores, a log flush), then hands them to
// Run. Run starts every component, blocks until its context is cancelled (a
// SIGINT/SIGTERM the main caught with signal.NotifyContext) or a component fails
// on its own, then stops every component under one shared deadline and finally
// runs the cleanups — so in-flight work drains before any resource it depends on
// is closed.
package lifecycle

import (
	"context"
	"sync"
	"time"
)

// DefaultShutdownTimeout is the deadline a daemon gives its components to drain
// in-flight work once shutdown is requested. It is the conventional value mains
// pass to Run; a component that cannot drain within it is force-released.
const DefaultShutdownTimeout = 30 * time.Second

// Component is one long-running part of a daemon. Start runs it and blocks until
// it stops; Shutdown asks it to stop gracefully within ctx's deadline. Start is
// expected to return once Shutdown has been invoked, and that post-shutdown
// return (for example http.ErrServerClosed, or a "use of closed network
// connection" error from a closed listener) is the normal path, not a failure —
// Run only treats a Start that returns while shutdown has NOT been requested as a
// genuine failure.
type Component interface {
	Start() error
	Shutdown(ctx context.Context) error
}

// Func adapts a pair of functions to a Component, for callers without a natural
// Component type and for tests.
type Func struct {
	StartFn    func() error
	ShutdownFn func(context.Context) error
}

// Start runs the component by calling StartFn.
func (f Func) Start() error { return f.StartFn() }

// Shutdown stops the component by calling ShutdownFn.
func (f Func) Shutdown(ctx context.Context) error { return f.ShutdownFn(ctx) }

// Run starts every component (each on its own goroutine), then blocks until ctx
// is cancelled or a component's Start returns while ctx is still live (a genuine
// failure). It then shuts every component down concurrently under a fresh
// timeout-bounded context, waits for all to return, and finally runs cleanups in
// the given order — strictly after every Shutdown has returned, so a cleanup
// never closes a resource an in-flight handler still needs. It returns the first
// meaningful error: a genuine Start failure, otherwise the first Shutdown or
// cleanup error.
func Run(ctx context.Context, timeout time.Duration, components []Component, cleanups ...func() error) error {
	errc := make(chan error, len(components))
	for _, c := range components {
		go func() { errc <- c.Start() }()
	}

	var runErr error
	select {
	case <-ctx.Done():
		// Graceful shutdown was requested; component Start returns are expected.
	case err := <-errc:
		// A Start returned before shutdown was requested — a genuine failure.
		runErr = err
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	shutErrs := make([]error, len(components))
	var wg sync.WaitGroup
	for i, c := range components {
		wg.Go(func() {
			shutErrs[i] = c.Shutdown(shutCtx)
		})
	}
	wg.Wait()

	// Cleanups run strictly after every Shutdown has returned (drain-then-close).
	for _, cl := range cleanups {
		if err := cl(); err != nil && runErr == nil {
			runErr = err
		}
	}
	for _, err := range shutErrs {
		if err != nil && runErr == nil {
			runErr = err
		}
	}
	return runErr
}
