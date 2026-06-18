package lifecycle_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"hermex/internal/lifecycle"
)

// TestConnGroupDrainsInFlight proves Shutdown lets an in-flight connection finish
// its handler instead of cutting it off, and stops accepting new connections: a
// connection enters a blocked handler, Shutdown is started, and only then is the
// handler released — the connection must still receive its response, Shutdown and
// Start must both report success, and a later dial must be refused.
func TestConnGroupDrainsInFlight(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	handle := func(nc net.Conn) {
		entered <- struct{}{}
		<-release
		nc.Write([]byte("drained"))
		nc.Close()
	}

	var g lifecycle.ConnGroup
	g.AddListener(ln)
	startDone := make(chan error, 1)
	go func() { startDone <- g.Start(handle) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	<-entered // the handler is now in-flight

	shutDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutDone <- g.Shutdown(ctx)
	}()
	time.Sleep(50 * time.Millisecond) // let Shutdown begin while the handler is still blocked
	close(release)

	buf := make([]byte, len("drained"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("in-flight connection did not complete: %v", err)
	}
	if string(buf) != "drained" {
		t.Errorf("in-flight response = %q, want \"drained\"", buf)
	}
	if err := <-shutDone; err != nil {
		t.Errorf("Shutdown = %v, want nil after draining", err)
	}
	if err := <-startDone; err != nil {
		t.Errorf("Start = %v, want nil after a graceful stop", err)
	}
	if c, err := net.Dial("tcp", ln.Addr().String()); err == nil {
		c.Close()
		t.Error("listener still accepting connections after Shutdown")
	}
}

// TestConnGroupShutdownTimeout proves Shutdown honors its deadline rather than
// blocking forever on a handler that will not drain — the select on ctx.Done(),
// not a bare WaitGroup wait.
func TestConnGroupShutdownTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	stuck := make(chan struct{})
	defer close(stuck) // release the stuck handler when the test ends

	var g lifecycle.ConnGroup
	g.AddListener(ln)
	go g.Start(func(nc net.Conn) {
		entered <- struct{}{}
		<-stuck
		nc.Close()
	})

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = g.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown = %v, want context.DeadlineExceeded when a handler will not drain", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Shutdown blocked %v, far past its 50ms deadline", d)
	}
}
