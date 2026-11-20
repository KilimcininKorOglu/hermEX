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
	return hostCharsetOK(host)
}

// hostCharsetOK guards the labels so nothing exotic reaches a URL: each label is
// 1 to 63 letters, digits and hyphens (RFC 1034 3.1) and does not start or end
// with a hyphen (RFC 1123 2.1), so a name of dots or an empty label is refused.
func hostCharsetOK(host string) bool {
	for label := range strings.SplitSeq(host, ".") {
		if !labelOK(label) {
			return false
		}
	}
	return true
}

// labelOK reports whether one DNS label meets the rule hostCharsetOK applies.
func labelOK(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}
