package ssrfguard

import (
	"net"
	"testing"
)

// TestSpecialPurposeAddressesAreRefused takes one address from every block the
// IANA IPv4 and IPv6 Special-Purpose Address Registries mark "Globally
// Reachable: False" (RFC 6890), plus IPv6 outside the 2000::/3 global unicast
// space, which carries the deprecated IPv4-compatible and SIIT forms of an
// internal address. None of them is a destination a callback may be sent to.
func TestSpecialPurposeAddressesAreRefused(t *testing.T) {
	for _, s := range []string{
		"0.1.2.3",         // 0.0.0.0/8 "This network"
		"192.0.0.1",       // 192.0.0.0/24 IETF Protocol Assignments
		"192.0.0.8",       // 192.0.0.8/32 IPv4 dummy address
		"192.0.0.170",     // NAT64/DNS64 discovery
		"192.0.2.1",       // TEST-NET-1
		"192.88.99.2",     // 6a44 relay anycast
		"198.18.0.1",      // 198.18.0.0/15 benchmarking
		"198.51.100.1",    // TEST-NET-2
		"240.0.0.1",       // 240.0.0.0/4 reserved
		"255.255.255.255", // limited broadcast
		"::127.0.0.1",     // IPv4-compatible, outside 2000::/3
		"::ffff:0:127.0.0.1",
		"fec0::1",                              // deprecated site-local, outside 2000::/3
		"100::1",                               // 100::/64 discard-only
		"2001::1",                              // 2001::/23 IETF protocol assignments
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", // Teredo, inside 2001::/23
		"2001:2::1",                            // benchmarking
		"2001:db8::1",                          // documentation
		"3fff::1",                              // documentation
		"5f00::1",                              // SRv6 SIDs
	} {
		if IsPublicIP(net.ParseIP(s)) {
			t.Errorf("IsPublicIP(%s) = true, want false (not globally reachable)", s)
		}
	}
}

// TestGloballyReachableCarveOutsStayPublic: the registries mark these blocks
// "Globally Reachable: True" inside larger blocks that are not, so they must stay
// reachable, as must ordinary global unicast.
func TestGloballyReachableCarveOutsStayPublic(t *testing.T) {
	for _, s := range []string{
		"192.0.0.9", "192.0.0.10", // PCP and TURN anycast
		"2001:1::1", "2001:1::3", "2001:3::1", "2001:4:112::1", "2001:20::1", "2001:30::1",
		"192.31.196.1", "8.8.8.8", "2606:4700:4700::1111",
	} {
		if !IsPublicIP(net.ParseIP(s)) {
			t.Errorf("IsPublicIP(%s) = false, want true (globally reachable)", s)
		}
	}
}
