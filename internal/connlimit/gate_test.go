package connlimit

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// addrConn is a connection that only reports a remote address, all ClientKey reads.
type addrConn struct {
	net.Conn
	remote net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.remote }

// TestARefusedConnectionIsToldWhyAndClosed proves the refusal path end to end:
// the operator's hook learns the client and which cap refused it, and the client
// reads the protocol's refusal line and then end of stream, so it backs off
// instead of waiting on a greeting that never comes. The gate is the cap the
// limiter holds (total, then per client), never a guess.
func TestARefusedConnectionIsToldWhyAndClosed(t *testing.T) {
	const line = "* BYE too many connections\r\n"
	l := enabled(t, 1, 1)
	var gotRemote string
	var gotWhy Reason
	failed := make(chan error, 1)
	gate, refuse := Gate(l, line, func(remote string, why Reason) { gotRemote, gotWhy = remote, why },
		func(_ string, err error) { failed <- err })
	remote := &net.TCPAddr{IP: net.ParseIP("198.51.100.7"), Port: 4242}

	if _, ok := gate(addrConn{remote: remote}); !ok {
		t.Fatal("the first connection was refused under the cap")
	}
	server, client := net.Pipe()
	t.Cleanup(func() { closeConn(t, client) })
	if _, ok := gate(addrConn{Conn: server, remote: remote}); ok {
		t.Fatal("a connection past the cap was admitted")
	}
	if gotRemote != remote.String() || gotWhy != ReasonTotal {
		t.Errorf("onRefused(%q, %q), want (%q, %q)", gotRemote, gotWhy, remote, ReasonTotal)
	}
	go refuse(server)
	got, err := io.ReadAll(client)
	if err != nil || string(got) != line {
		t.Errorf("client read %q, %v, want the refusal line then end of stream", got, err)
	}
	select {
	case err := <-failed:
		t.Errorf("a refusal the client read was reported as failed: %v", err)
	default:
	}
}

// closeConn closes a test connection and fails the test when it cannot.
func closeConn(t *testing.T, c net.Conn) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestRefusalToAClientThatNeverReadsReturns refuses a client that never reads.
// The write must give up at its deadline and close the connection, or one stalled
// client pins a goroutine and a socket per refused connection. The refusal that
// never arrived is reported, so the operator sees it.
func TestRefusalToAClientThatNeverReadsReturns(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { closeConn(t, client) })
	failed := make(chan error, 1)
	_, refuse := Gate(New(), "* BYE too many connections\r\n", nil, func(_ string, err error) { failed <- err })
	done := make(chan struct{})
	go func() { refuse(server); close(done) }()
	select {
	case <-done:
	case <-time.After(refusalWriteTimeout + 5*time.Second):
		t.Fatal("refuse is still blocked on a client that never reads")
	}
	if _, err := server.Write([]byte("x")); err == nil {
		t.Error("the refused connection is still open")
	}
	select {
	case err := <-failed:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("the reported refusal failure is %v, want the write deadline", err)
		}
	default:
		t.Error("a refusal that never reached the client was not reported")
	}
}

// closeFails is a connection whose Close fails, as a socket the kernel refuses
// to release does.
type closeFails struct {
	net.Conn
	err error
}

func (c closeFails) Close() error { return errors.Join(c.err, c.Conn.Close()) }

// TestAFailedCloseOfARefusedConnectionIsReported: the refusal reached the client,
// but the connection did not close, so the operator must learn of the socket.
func TestAFailedCloseOfARefusedConnectionIsReported(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { closeConn(t, client) })
	closeErr := errors.New("close refused")
	var got error
	_, refuse := Gate(New(), "-ERR busy\r\n", nil, func(_ string, err error) { got = err })
	type readResult struct {
		b   []byte
		err error
	}
	read := make(chan readResult, 1)
	go func() {
		b, err := io.ReadAll(client)
		read <- readResult{b, err}
	}()
	refuse(closeFails{Conn: server, err: closeErr})
	if r := <-read; r.err != nil || string(r.b) != "-ERR busy\r\n" {
		t.Errorf("client read %q, %v; want the refusal line then end of stream", r.b, r.err)
	}
	if !errors.Is(got, closeErr) {
		t.Errorf("reported %v, want the close error", got)
	}
}

// TestPerClientCapCountsAnIPv6SubnetAsOneClient opens connections from a fresh
// address of one /64 each time. A host is routed a whole /64 (RFC 6177), so keyed
// per address it held as many connections as it liked; the per-client cap must
// refuse the third from that /64 and still admit a neighbouring /64.
func TestPerClientCapCountsAnIPv6SubnetAsOneClient(t *testing.T) {
	l := enabled(t, 100, 2)
	from := func(ip string) net.Conn {
		return addrConn{remote: &net.TCPAddr{IP: net.ParseIP(ip), Port: 993}}
	}
	gate, _ := Gate(l, "", nil, nil)
	for _, ip := range []string{"2001:db8:1:2::1", "2001:db8:1:2::2"} {
		if _, ok := gate(from(ip)); !ok {
			t.Fatalf("connection from %s refused under the cap", ip)
		}
	}
	if _, ok := gate(from("2001:db8:1:2:ffff::3")); ok {
		t.Error("a third connection from the same /64 was admitted past the per-client cap")
	}
	if _, ok := gate(from("2001:db8:1:3::1")); !ok {
		t.Error("a connection from the neighbouring /64 was refused")
	}
}
