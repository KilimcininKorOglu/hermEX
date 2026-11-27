package lifecycle_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"hermex/internal/lifecycle"
)

// closeOrFail closes c and reports any unexpected error.
func closeOrFail(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("close: %v", err)
	}
}

// writeAndClose writes msg to nc and closes it, reporting any error.
func writeAndClose(t *testing.T, nc net.Conn, msg string) {
	t.Helper()
	if _, err := nc.Write([]byte(msg)); err != nil {
		t.Errorf("write %q: %v", msg, err)
	}
	closeOrFail(t, nc)
}

// gatedGroup serves one listener with the given gate and refusal writer, and
// returns the listener plus a count of the handlers that ran.
func gatedGroup(t *testing.T, gate lifecycle.Gate, refuse func(net.Conn)) (net.Listener, *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var served atomic.Int64
	var g lifecycle.ConnGroup
	g.AddListener(ln)
	g.SetGate(gate, refuse)
	startErr := make(chan error, 1)
	go func() {
		startErr <- g.Start(func(nc net.Conn) {
			served.Add(1)
			writeAndClose(t, nc, "served")
		})
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := g.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if err := <-startErr; err != nil {
			t.Errorf("start: %v", err)
		}
	})
	return ln, &served
}

// readAll dials the listener and returns everything the server wrote.
func readAll(t *testing.T, ln net.Listener) string {
	t.Helper()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer closeOrFail(t, conn)
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set the read deadline: %v", err)
	}
	b, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

// TestGateRefusesWithoutAHandler is the load-bearing case: a refused connection
// gets the refusal text and no handler runs for it, which is what makes the cap
// bound the daemon's work rather than only its answer.
func TestGateRefusesWithoutAHandler(t *testing.T) {
	gate := func(net.Conn) (func(), bool) { return nil, false }
	refuse := func(nc net.Conn) {
		writeAndClose(t, nc, "refused")
	}
	ln, served := gatedGroup(t, gate, refuse)

	if got := readAll(t, ln); got != "refused" {
		t.Errorf("refused connection read %q, want the refusal text", got)
	}
	if n := served.Load(); n != 0 {
		t.Errorf("handlers run = %d, want 0 for a refused connection", n)
	}
}

// TestGateReleasesWhenTheHandlerReturns proves the slot a served connection took
// is given back, so the cap does not leak a slot per connection.
func TestGateReleasesWhenTheHandlerReturns(t *testing.T) {
	var released atomic.Int64
	gate := func(net.Conn) (func(), bool) {
		return func() { released.Add(1) }, true
	}
	ln, served := gatedGroup(t, gate, nil)

	if got := readAll(t, ln); got != "served" {
		t.Fatalf("admitted connection read %q, want the handler's answer", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for released.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if released.Load() != 1 || served.Load() != 1 {
		t.Errorf("served = %d, released = %d, want 1 and 1", served.Load(), released.Load())
	}
}

// TestNoGateServesEveryConnection proves a group without a gate behaves exactly as
// it did before the gate existed.
func TestNoGateServesEveryConnection(t *testing.T) {
	ln, served := gatedGroup(t, nil, nil)

	if got := readAll(t, ln); got != "served" {
		t.Errorf("ungated connection read %q, want the handler's answer", got)
	}
	if n := served.Load(); n != 1 {
		t.Errorf("handlers run = %d, want 1", n)
	}
}

// TestAStalledRefusalDoesNotStopTheAcceptLoop refuses a client whose refusal
// never finishes, as a client that never reads would hold a refusal write. The
// next client must still be served: a refusal on the accept loop itself would let
// one such client shut the daemon to everyone, which is the flood the cap guards.
func TestAStalledRefusalDoesNotStopTheAcceptLoop(t *testing.T) {
	stall := make(chan struct{})
	var calls atomic.Int64
	gate := func(net.Conn) (func(), bool) { return nil, calls.Add(1) > 1 }
	refuse := func(nc net.Conn) {
		<-stall
		closeOrFail(t, nc)
	}
	ln, served := gatedGroup(t, gate, refuse)
	// Register after gatedGroup so LIFO unblocks the stalled refusal before Shutdown drains.
	t.Cleanup(func() { close(stall) })

	first, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer closeOrFail(t, first)
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	if got := readAll(t, ln); got != "served" {
		t.Errorf("second connection read %q, want it served while the first refusal stalls", got)
	}
	if n := served.Load(); n != 1 {
		t.Errorf("handlers run = %d, want 1", n)
	}
}

// TestAPanickingHandlerReleasesItsSlot proves a handler that panics still gives
// its slot back. Otherwise every panicking command would leak one slot until the
// cap refuses all clients.
func TestAPanickingHandlerReleasesItsSlot(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{}, 1)
	var g lifecycle.ConnGroup
	g.AddListener(ln)
	g.SetGate(func(net.Conn) (func(), bool) { return func() { released <- struct{}{} }, true }, nil)
	startErr := make(chan error, 1)
	go func() { startErr <- g.Start(func(net.Conn) { panic("bad command") }) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := g.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if err := <-startErr; err != nil {
			t.Errorf("start: %v", err)
		}
	})

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer closeOrFail(t, conn)

	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("the slot of a panicking handler was never released")
	}
}

// TestRefusedConnectionClosesWithoutARefusalWriter proves a gate installed with no
// refusal writer still closes the connection rather than leaking the socket.
func TestRefusedConnectionClosesWithoutARefusalWriter(t *testing.T) {
	gate := func(net.Conn) (func(), bool) { return nil, false }
	ln, served := gatedGroup(t, gate, nil)

	if got := readAll(t, ln); got != "" {
		t.Errorf("refused connection read %q, want nothing before the close", got)
	}
	if n := served.Load(); n != 0 {
		t.Errorf("handlers run = %d, want 0", n)
	}
}

// TestGateAdmitsWithNilRelease proves a gate that admits a connection with a nil
// release function does not panic on handler return and still serves the client.
func TestGateAdmitsWithNilRelease(t *testing.T) {
	gate := func(net.Conn) (func(), bool) { return nil, true }
	ln, served := gatedGroup(t, gate, nil)

	if got := readAll(t, ln); got != "served" {
		t.Errorf("admitted connection read %q, want %q", got, "served")
	}
	if n := served.Load(); n != 1 {
		t.Errorf("handlers run = %d, want 1", n)
	}
}
