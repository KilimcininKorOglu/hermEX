package lifecycle

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// dialOnce connects to the listener, writes one byte so the handler runs, and
// returns the connection.
func dialOnce(t *testing.T, ln net.Listener) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	return c
}

// panicReport is one recovered panic as the group reported it.
type panicReport struct {
	mu     sync.Mutex
	remote string
	value  any
	stack  string
	seen   chan struct{}
}

// record is the reporter a ConnGroup is given.
func (p *panicReport) record(remote string, v any, stack []byte) {
	p.mu.Lock()
	p.remote, p.value, p.stack = remote, v, string(stack)
	p.mu.Unlock()
	select {
	case p.seen <- struct{}{}:
	default:
	}
}

// read returns the recorded report.
func (p *panicReport) read() (string, any, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.remote, p.value, p.stack
}

// echoOrPanic panics on the byte 'x' and echoes anything else, so one test can
// drive both a failing and a healthy connection through the same group.
func echoOrPanic(served chan struct{}) func(net.Conn) {
	return func(nc net.Conn) {
		buf := make([]byte, 1)
		if _, err := nc.Read(buf); err != nil {
			return
		}
		if buf[0] == 'x' {
			panic("a command the handler could not take")
		}
		_, _ = nc.Write([]byte("ok"))
		select {
		case served <- struct{}{}:
		default:
		}
		_ = nc.Close()
	}
}

// waitFor fails the test when the channel does not fire in time.
func waitFor(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal(what)
	}
}

// TestAPanickingHandlerLosesOnlyItsConnection is the load-bearing case: a
// connection-oriented daemon serves every client from one goroutine per
// connection, and Go does not recover a panic there the way net/http does for an
// HTTP handler. Without the guard, one malformed command from one client ends the
// process and every other client's session with it.
func TestAPanickingHandlerLosesOnlyItsConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	report := &panicReport{seen: make(chan struct{}, 1)}
	served := make(chan struct{}, 4)
	var g ConnGroup
	g.SetPanicHandler(report.record)
	go func() { _ = g.Serve(ln, echoOrPanic(served)) }()

	// The first client panics its handler.
	dialOnce(t, ln)
	waitFor(t, report.seen, "the panic was never reported")

	remote, value, stack := report.read()
	if remote == "" {
		t.Error("the report names no client address")
	}
	if s, _ := value.(string); !strings.Contains(s, "could not take") {
		t.Errorf("the report carries %v, want the panic value", value)
	}
	if !strings.Contains(stack, "lifecycle") {
		t.Errorf("the report carries no usable stack: %q", stack)
	}

	// The listener still serves: the process survived the panic.
	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("the group stopped accepting after a panic: %v", err)
	}
	defer c2.Close()
	if _, err := c2.Write([]byte("y")); err != nil {
		t.Fatalf("write to the second connection: %v", err)
	}
	waitFor(t, served, "a later connection was not served after the panic")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.Shutdown(ctx); err != nil {
		t.Errorf("shutdown after a recovered panic: %v", err)
	}
}

// TestAPanickingHandlerClosesItsConnection proves the client is not left hanging on
// a connection whose handler is gone.
func TestAPanickingHandlerClosesItsConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var g ConnGroup
	go func() {
		_ = g.Serve(ln, func(nc net.Conn) {
			buf := make([]byte, 1)
			if _, err := nc.Read(buf); err != nil {
				return
			}
			panic("boom")
		})
	}()

	c := dialOnce(t, ln)
	if err := c.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Error("the connection stayed open after its handler panicked")
	}
}

// TestAPanicWithNoHandlerStillRecovers keeps a server that installed no reporter
// from dying: the report is optional, the recovery is not.
func TestAPanicWithNoHandlerStillRecovers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var g ConnGroup
	done := make(chan struct{}, 1)
	go func() {
		_ = g.Serve(ln, func(nc net.Conn) {
			defer func() {
				select {
				case done <- struct{}{}:
				default:
				}
			}()
			panic("boom")
		})
	}()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler never ran")
	}
}
