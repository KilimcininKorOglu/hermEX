package lifecycle

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
)

// failingCloseConn is a connection whose Close reports err.
type failingCloseConn struct {
	net.Conn
	err error
}

func (c failingCloseConn) Close() error { return c.err }
func (c failingCloseConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(192, 0, 2, 7), Port: 1}
}

// captureSlog sends the default slog output to a buffer for the test's life.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestCloseConnReportsAFailedClose follows the fail-fast rule that no error is
// hidden: a Close that fails is written at a level the default handler shows
// (warn, above the default info threshold) with the peer address, and a
// connection that is already closed (net.ErrClosed) is not reported.
func TestCloseConnReportsAFailedClose(t *testing.T) {
	buf := captureSlog(t)
	closeConn(failingCloseConn{err: errors.New("reset by peer")})
	out := buf.String()
	for _, want := range []string{"level=WARN", "reset by peer", "192.0.2.7"} {
		if !strings.Contains(out, want) {
			t.Errorf("close failure log = %q, want it to carry %q", out, want)
		}
	}

	buf.Reset()
	closeConn(failingCloseConn{err: net.ErrClosed})
	if buf.Len() != 0 {
		t.Errorf("an already-closed conn was reported: %q", buf.String())
	}
}
