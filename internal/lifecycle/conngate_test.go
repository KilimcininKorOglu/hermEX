package lifecycle_test

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"hermex/internal/lifecycle"
)

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
	go func() {
		_ = g.Start(func(nc net.Conn) {
			served.Add(1)
			_, _ = nc.Write([]byte("served"))
			_ = nc.Close()
		})
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln, &served
}

// readAll dials the listener and returns everything the server wrote.
func readAll(t *testing.T, ln net.Listener) string {
	t.Helper()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
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
		_, _ = nc.Write([]byte("refused"))
		_ = nc.Close()
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
