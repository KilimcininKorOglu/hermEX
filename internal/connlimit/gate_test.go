package connlimit

import (
	"io"
	"net"
	"testing"
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
	gate, refuse := Gate(l, line, func(remote string, why Reason) { gotRemote, gotWhy = remote, why })
	remote := &net.TCPAddr{IP: net.ParseIP("198.51.100.7"), Port: 4242}

	if _, ok := gate(addrConn{remote: remote}); !ok {
		t.Fatal("the first connection was refused under the cap")
	}
	server, client := net.Pipe()
	defer client.Close()
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
	gate, _ := Gate(l, "", nil)
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
