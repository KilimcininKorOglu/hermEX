package ssrfguard

import (
	"net"
	"testing"
)

// TestSharedAddressSpaceIsRefused is the deny-list gap. The standard library's
// private-range test covers RFC 1918 and RFC 4193 only, so RFC 6598 carrier-grade
// NAT space reads as public, while cloud providers use it for internal NAT
// gateways, load balancers and container networks. A callback resolving into it
// was dialed as if it were on the internet.
func TestSharedAddressSpaceIsRefused(t *testing.T) {
	for _, addr := range []string{"100.64.0.1", "100.100.50.7", "100.127.255.254"} {
		if IsPublicIP(net.ParseIP(addr)) {
			t.Errorf("%s was treated as public; RFC 6598 space must be refused", addr)
		}
	}
}

// TestAddressesEitherSideOfSharedSpaceStayPublic pins the boundary, so the new
// range cannot quietly swallow ordinary public addresses next to it.
func TestAddressesEitherSideOfSharedSpaceStayPublic(t *testing.T) {
	for _, addr := range []string{"100.63.255.255", "100.128.0.0", "93.184.216.34"} {
		if !IsPublicIP(net.ParseIP(addr)) {
			t.Errorf("%s was refused; it is outside RFC 6598 space and must stay public", addr)
		}
	}
}
