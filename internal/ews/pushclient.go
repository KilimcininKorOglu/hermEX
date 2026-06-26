package ews

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Push-callback delivery tuning.
const (
	pushPostTimeout  = 10 * time.Second // total deadline for one SendNotification POST
	pushDialTimeout  = 5 * time.Second
	pushMaxFailures  = 5 // consecutive POST failures before the subscription is dropped
	pushMaxRespBytes = 64 << 10
)

// validateCallbackURL checks a client-supplied push callback URL before it is ever
// dialed. The URL is attacker-controlled (the client names it in the Subscribe
// request), so this is the first SSRF gate: only an absolute http(s) URL with a
// host is accepted, and plaintext http is refused unless explicitly allowed (an
// internal/dev deployment). The per-request IP-range block is enforced separately
// at dial time, where the resolved address is known.
func validateCallbackURL(raw string, allowHTTP bool) error {
	if raw == "" {
		return errors.New("push subscription requires a callback URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid callback URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !allowHTTP {
			return errors.New("push callback must use https")
		}
	default:
		return fmt.Errorf("unsupported callback scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("push callback URL has no host")
	}
	return nil
}

// isPublicIP reports whether ip is a routable, non-internal address a push callback
// is permitted to reach. It rejects loopback (127/8, ::1), link-local (169.254/16
// incl. the 169.254.169.254 cloud-metadata address, fe80::/10), private (10/8,
// 172.16/12, 192.168/16, fc00::/7), unspecified (0.0.0.0, ::), and multicast — the
// SSRF address space an attacker-named callback would try to pivot into.
func isPublicIP(ip net.IP) bool {
	return !(ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsMulticast())
}

// ssrfGuardedDial returns a DialContext that resolves the target host and refuses
// the connection if ANY resolved address is non-public (so a name that resolves to
// both a public and an internal IP cannot be used to pivot), then dials a validated
// address directly — closing the DNS-rebinding window between validation and dial.
// TLS verification still uses the request's hostname (the transport sets ServerName
// from the URL), so dialing the IP does not weaken certificate checks. allowInternal
// disables the IP-range block for an internal/dev deployment (or a test).
func ssrfGuardedDial(allowInternal bool) func(context.Context, string, string) (net.Conn, error) {
	d := &net.Dialer{Timeout: pushDialTimeout}
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
			return nil, fmt.Errorf("ews push: callback host %q did not resolve", host)
		}
		if !allowInternal {
			for _, ip := range ips {
				if !isPublicIP(ip) {
					return nil, fmt.Errorf("ews push: refusing callback to non-public address %s", ip)
				}
			}
		}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
}

// pushClient builds the HTTP client used for SendNotification callbacks: a strict
// timeout, no redirect following (a redirect could escape the SSRF guard), and the
// IP-range-guarded dialer.
func pushClient(allowInternal bool) *http.Client {
	return &http.Client{
		Timeout: pushPostTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("ews push: callback redirects are not followed")
		},
		Transport: &http.Transport{
			DialContext:         ssrfGuardedDial(allowInternal),
			TLSHandshakeTimeout: pushDialTimeout,
			DisableKeepAlives:   true,
		},
	}
}

// sendNotificationResultEnvelope extracts the SubscriptionStatus the client returns
// from its callback. The path matches regardless of namespace prefixes (Go's xml
// path matching uses local names).
type sendNotificationResultEnvelope struct {
	XMLName xml.Name
	Status  string `xml:"Body>SendNotificationResult>SubscriptionStatus"`
}

// deliverPush POSTs body to the callback URL and reports whether to keep the
// subscription. A transport error (including an SSRF-guard refusal) is returned so
// the caller can count it toward the failure budget; a parsed SubscriptionStatus of
// "Unsubscribe" returns keep=false with no error (the client asked to stop). A
// missing or "OK" status keeps the subscription.
func (s *Server) deliverPush(callbackURL string, body []byte) (keep bool, err error) {
	req, err := http.NewRequest(http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	resp, err := s.pushHTTP.Do(req)
	if err != nil {
		return true, err // transient/guard failure: count it, do not unsubscribe yet
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return true, fmt.Errorf("ews push: callback returned status %d", resp.StatusCode)
	}
	var env sendNotificationResultEnvelope
	if err := xml.NewDecoder(io.LimitReader(resp.Body, pushMaxRespBytes)).Decode(&env); err != nil {
		return true, nil // unparseable but delivered: keep the subscription
	}
	return !strings.EqualFold(env.Status, "Unsubscribe"), nil
}
