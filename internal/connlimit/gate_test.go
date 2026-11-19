package connlimit

import (
	"net"
	"testing"
)

// addrConn is a connection that only reports a remote address, all ClientKey reads.
type addrConn struct {
	net.Conn
	remote net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.remote }

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
