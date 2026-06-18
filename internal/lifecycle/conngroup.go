package lifecycle

import (
	"context"
	"net"
	"sync"
)

// ConnGroup serves connections from a set of listeners with a per-connection
// handler, tracking the in-flight handlers so Shutdown can drain them. The zero
// value is ready to use. It is meant to be embedded by a connection-oriented
// protocol server (IMAP/POP3/SMTP), which exposes its own Start/Shutdown in terms
// of these so it satisfies Component while passing its own handle method.
//
// Shutdown closes every registered listener (so the accept loops stop) and then
// waits for the active handlers under the caller's deadline. New handlers can
// only start while not draining — the draining flag and the wait-group's Add are
// serialized by the same mutex, so no Add ever races the drain's Wait.
type ConnGroup struct {
	mu        sync.Mutex
	listeners []net.Listener
	draining  bool
	handlers  sync.WaitGroup
}

// AddListener registers l to be served by Start. Call it before Start.
func (g *ConnGroup) AddListener(l net.Listener) {
	g.mu.Lock()
	g.listeners = append(g.listeners, l)
	g.mu.Unlock()
}

// Start serves every registered listener concurrently, dispatching each
// connection to handle, and blocks until all of them stop (via Shutdown). It
// returns the first non-shutdown accept error.
func (g *ConnGroup) Start(handle func(net.Conn)) error {
	g.mu.Lock()
	ls := append([]net.Listener(nil), g.listeners...)
	g.mu.Unlock()

	errc := make(chan error, len(ls))
	var wg sync.WaitGroup
	for _, l := range ls {
		wg.Go(func() { errc <- g.Serve(l, handle) })
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			return err
		}
	}
	return nil
}

// Serve accepts connections on l, dispatching each to handle in a tracked
// goroutine, until l is closed. It returns nil once Shutdown has closed the
// listener (the expected stop) and the accept error otherwise.
func (g *ConnGroup) Serve(l net.Listener, handle func(net.Conn)) error {
	for {
		nc, err := l.Accept()
		if err != nil {
			if g.isDraining() {
				return nil
			}
			return err
		}
		g.mu.Lock()
		if g.draining {
			g.mu.Unlock()
			nc.Close()
			return nil
		}
		g.handlers.Add(1)
		g.mu.Unlock()
		go func() {
			defer g.handlers.Done()
			handle(nc)
		}()
	}
}

func (g *ConnGroup) isDraining() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.draining
}

// Shutdown stops accepting — closing every registered listener — and drains the
// in-flight handlers, giving up when ctx's deadline passes.
func (g *ConnGroup) Shutdown(ctx context.Context) error {
	g.mu.Lock()
	g.draining = true
	for _, l := range g.listeners {
		l.Close()
	}
	g.mu.Unlock()

	done := make(chan struct{})
	go func() {
		g.handlers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
