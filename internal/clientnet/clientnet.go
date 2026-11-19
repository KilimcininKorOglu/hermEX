// Package clientnet names the client a remote address belongs to, the one key
// every per-client limiter counts against (failed logins, open connections,
// HTTP requests), so all of them agree on what "one client" is.
package clientnet

import "net"

// ipv6ClientBits is the IPv6 prefix one subscriber is routed: RFC 6177 assigns a
// site at least a /64, so every address inside it is the same client.
const ipv6ClientBits = 64

// Key reduces a remote address ("ip:port", or a bare ip) to the client it names.
// The port is dropped, because a client opens a fresh source port per connection.
// An IPv4 address, also when written IPv4-mapped, keys on itself. An IPv6 address
// keys on its /64: a single host may pick any of 2^64 addresses there, so counted
// per address it would never reach a limit, and its fresh keys would fill a
// bounded table until the limiter stopped tracking anyone. An address that does
// not parse is returned without its port, and an empty one as empty.
func Key(remoteAddr string) string {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		return host
	case ip.To4() != nil:
		return ip.To4().String()
	default:
		return ip.Mask(net.CIDRMask(ipv6ClientBits, 128)).String() + "/64"
	}
}
