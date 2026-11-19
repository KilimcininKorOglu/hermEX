package ssrfguard

import (
	"net"
	"testing"
)

// TestIPv6EmbeddingAnInternalIPv4IsRefused names internal IPv4 hosts through the
// IPv6 forms that carry an IPv4 address. On a network with NAT64, 64:ff9b::/96
// (RFC 6052 2.1) reaches the embedded address, and a 6to4 address 2002::/16
// (RFC 3056 2) is routed to its embedded one; RFC 8215's 64:ff9b:1::/48 is
// reserved for local NAT64 use, so it is internal by definition. Each must be
// refused as the IPv4 address it names would be.
func TestIPv6EmbeddingAnInternalIPv4IsRefused(t *testing.T) {
	for _, addr := range []string{
		"64:ff9b::7f00:1",    // 127.0.0.1
		"64:ff9b::a9fe:a9fe", // 169.254.169.254
		"64:ff9b::a00:1",     // 10.0.0.1
		"2002:7f00:1::1",     // 6to4 of 127.0.0.1
		"2002:a9fe:a9fe::1",  // 6to4 of 169.254.169.254
		"64:ff9b:1::1",       // RFC 8215 local-use NAT64
	} {
		if IsPublicIP(net.ParseIP(addr)) {
			t.Errorf("%s was treated as public; it names an internal IPv4 host", addr)
		}
	}
}

// TestIPv6EmbeddingAPublicIPv4StaysPublic is the negative control: the same
// forms around a public IPv4 address are ordinary internet destinations.
func TestIPv6EmbeddingAPublicIPv4StaysPublic(t *testing.T) {
	for _, addr := range []string{
		"64:ff9b::5db8:d822", // 93.184.216.34
		"2002:5db8:d822::1",  // 6to4 of 93.184.216.34
	} {
		if !IsPublicIP(net.ParseIP(addr)) {
			t.Errorf("%s was refused; it embeds a public IPv4 address", addr)
		}
	}
}
