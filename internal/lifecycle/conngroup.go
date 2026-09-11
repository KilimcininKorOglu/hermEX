package lifecycle

import (
	"context"
	"errors"
	"net"
	"runtime"
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
// only start while not draining, the draining flag and the wait-group's Add are
// serialized by the same mutex, so no Add ever races the drain's Wait.
type ConnGroup struct {
	mu        sync.Mutex
	listeners []net.Listener
	draining  bool
	handlers  sync.WaitGroup
	gate      Gate
	refuse    func(net.Conn)
	onPanic   func(remote string, v any, stack []byte)
}

// panicStackBytes bounds the stack trace a recovered panic records. A trace long
// enough to matter fits well inside it, and a bounded copy keeps one panic from
// writing a megabyte into the log store.
const panicStackBytes = 8 << 10

// SetPanicHandler installs the reporter for a panic a connection handler raised.
// Call it before Start. A nil reporter leaves the panic recorded nowhere, which is
// why every server that has a logger passes one.
func (g *ConnGroup) SetPanicHandler(onPanic func(remote string, v any, stack []byte)) {
	g.mu.Lock()
	g.onPanic = onPanic
	g.mu.Unlock()
}

// guard runs one connection handler, turning a panic into a closed connection and
// a reported event instead of a dead process.
//
// A connection-oriented daemon serves every client from this one goroutine per
// connection, and Go does not recover a panic for it the way net/http does for an
// HTTP handler. Without this, one malformed command from one client ends the
// process and every other client's session with it. The connection is closed,
// because a handler that panicked left the protocol at an unknown point and cannot
// answer the next command.
func (g *ConnGroup) guard(handle func(net.Conn), nc net.Conn) {
	defer func() {
		v := recover()
		if v == nil {
			return
		}
		_ = nc.Close()
		g.mu.Lock()
		onPanic := g.onPanic
		g.mu.Unlock()
		if onPanic == nil {
			return
		}
		buf := make([]byte, panicStackBytes)
		buf = buf[:runtime.Stack(buf, false)]
		onPanic(remoteAddr(nc), v, buf)
	}()
	handle(nc)
}

// remoteAddr names a connection's peer, tolerating a connection with none.
func remoteAddr(nc net.Conn) string {
	if a := nc.RemoteAddr(); a != nil {
		return a.String()
	}
	return ""
}

// Gate decides whether a newly accepted connection may be served. It returns the
// release to call once the handler has returned, and ok=false to refuse the
// connection. A nil gate admits everything.
type Gate func(net.Conn) (release func(), ok bool)

// SetGate installs the admission gate and the refusal writer used when the gate
// says no. refuse owns the connection it is given: it writes whatever the protocol
// says and closes it. Call SetGate before Start.
func (g *ConnGroup) SetGate(gate Gate, refuse func(net.Conn)) {
	g.mu.Lock()
	g.gate, g.refuse = gate, refuse
	g.mu.Unlock()
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
			return nc.Close() // rejecting a late connection during shutdown; nil unless the close failed
		}
		gate, refuse := g.gate, g.refuse
		g.handlers.Add(1)
		g.mu.Unlock()

		release, admitted := admit(gate, nc)
		if !admitted {
			// The refusal writes to the connection, so it runs on its own goroutine:
			// a client that never reads would otherwise stall the accept loop. It is
			// still tracked, so Shutdown waits for it.
			go func() {
				defer g.handlers.Done()
				refuseConn(refuse, nc)
			}()
			continue
		}
		go func() {
			defer g.handlers.Done()
			defer release()
			g.guard(handle, nc)
		}()
	}
}

// admit asks the gate whether a connection may be served. A nil gate admits
// everything with a no-op release.
func admit(gate Gate, nc net.Conn) (release func(), ok bool) {
	if gate == nil {
		return func() {}, true
	}
	release, ok = gate(nc)
	if ok && release == nil {
		release = func() {}
	}
	return release, ok
}

// refuseConn hands a refused connection to the refusal writer, and closes it
// itself when there is none.
func refuseConn(refuse func(net.Conn), nc net.Conn) {
	if refuse == nil {
		_ = nc.Close()
		return
	}
	refuse(nc)
}

func (g *ConnGroup) isDraining() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.draining
}

// Shutdown stops accepting, closing every registered listener, and drains the
// in-flight handlers, giving up when ctx's deadline passes.
func (g *ConnGroup) Shutdown(ctx context.Context) error {
	g.mu.Lock()
	g.draining = true
	var closeErr error
	for _, l := range g.listeners {
		closeErr = errors.Join(closeErr, l.Close())
	}
	g.mu.Unlock()

	done := make(chan struct{})
	go func() {
		g.handlers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return closeErr
	case <-ctx.Done():
		return errors.Join(closeErr, ctx.Err())
	}
}
