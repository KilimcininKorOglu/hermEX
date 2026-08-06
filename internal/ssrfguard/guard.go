// Package ssrfguard gates outbound HTTP to a URL a client chose. Several surfaces
// let a client register a URL the server later dials on its own schedule (an EWS
// push callback, a web-push subscription endpoint), which is a server-side request
// forgery lever unless the destination is checked. This package is the single
// implementation of that check, so a new surface reuses it rather than dialing with
// a bare http.Client.
package ssrfguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Outbound delivery tuning.
const (
	// PostTimeout is the total deadline for one guarded POST.
	PostTimeout = 10 * time.Second
	// DialTimeout bounds the connect and the TLS handshake.
	DialTimeout = 5 * time.Second
)

// ValidateURL checks a client-supplied URL before it is ever stored or dialed. Only
// an absolute http(s) URL with a host is accepted, and a host given as an address
// literal is range-checked here, where it is already known.
//
// allowInternal marks an internal or development deployment: it permits plaintext
// http and an internal address literal.
//
// This gate is not the whole defence. A host given as a name is checked at dial
// time by GuardedDial, where the resolved address is known, because a name that
// validates now can resolve somewhere else later. What this gate buys is keeping an
// obviously unusable destination out of the caller's store and answering plainly.
func ValidateURL(raw string, allowInternal bool) error {
	if raw == "" {
		return errors.New("a callback URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid callback URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !allowInternal {
			return errors.New("the callback must use https")
		}
	default:
		return fmt.Errorf("unsupported callback scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("the callback URL has no host")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !allowInternal && !IsPublicIP(ip) {
		return fmt.Errorf("the callback address %s is not public", ip)
	}
	return nil
}

// IsPublicIP reports whether ip is a routable, non-internal address a callback is
// permitted to reach. It rejects loopback (127/8, ::1), link-local (169.254/16
// including the 169.254.169.254 cloud-metadata address, fe80::/10), private (10/8,
// 172.16/12, 192.168/16, fc00::/7), unspecified (0.0.0.0, ::), and multicast: the
// address space an attacker-named callback would try to pivot into.
func IsPublicIP(ip net.IP) bool {
	return !(ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsMulticast())
}

// GuardedDial returns a DialContext that resolves the target host and refuses the
// connection if ANY resolved address is non-public (so a name that resolves to both
// a public and an internal address cannot be used to pivot), then dials a validated
// address directly, which closes the DNS-rebinding window between the check and the
// dial. TLS verification still uses the request's hostname (the transport sets
// ServerName from the URL), so dialing the address does not weaken certificate
// checks. allowInternal disables the range block for an internal or development
// deployment, or a test.
func GuardedDial(allowInternal bool) func(context.Context, string, string) (net.Conn, error) {
	d := &net.Dialer{Timeout: DialTimeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("ssrfguard: callback host %q did not resolve", host)
		}
		if !allowInternal {
			for _, ip := range ips {
				if !IsPublicIP(ip) {
					return nil, fmt.Errorf("ssrfguard: refusing callback to non-public address %s", ip)
				}
			}
		}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
}

// Client builds the HTTP client used for callbacks: a strict timeout, no redirect
// following (a redirect could escape the guard), and the range-guarded dialer.
func Client(allowInternal bool) *http.Client {
	return &http.Client{
		Timeout: PostTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("ssrfguard: callback redirects are not followed")
		},
		Transport: &http.Transport{
			DialContext:         GuardedDial(allowInternal),
			TLSHandshakeTimeout: DialTimeout,
			DisableKeepAlives:   true,
		},
	}
}
