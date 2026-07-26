package serve

import (
	"net"
	"strings"
)

// ValidPublicHost reports whether host (a request Host header, possibly with a
// port) is a plausible public FQDN safe to reflect into a client-facing URL such
// as an autodiscover response. It rejects an empty value, a bare IP literal, a
// loopback/private/link-local address, and any name without a dot (localhost and
// other single-label names). It is intentionally strict: the configured hostname
// is the authoritative source, and this only gates the client-supplied fallback.
func ValidPublicHost(host string) bool {
	if host == "" {
		return false
	}
	// Strip an optional :port. SplitHostPort fails when there is no port, in which
	// case the whole value is the host.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return false
	}
	// A bare IP literal is never an acceptable public autodiscover target: it
	// bypasses hostname-based trust and may point at an internal address.
	if ip := net.ParseIP(host); ip != nil {
		return false
	}
	host = strings.TrimSuffix(host, ".")
	// A public FQDN carries at least one dot; a single label (localhost, an
	// intranet short name) is rejected.
	if !strings.Contains(host, ".") {
		return false
	}
	// Guard the label charset so nothing exotic reaches a URL: letters, digits,
	// dot and hyphen only.
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.' || r == '-':
		default:
			return false
		}
	}
	return true
}
