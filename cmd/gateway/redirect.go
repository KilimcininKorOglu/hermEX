package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// httpsRedirect answers every plain-HTTP request with a 301 to the same host,
// path and query over https, so a browser that types the bare name or an
// http:// link reaches the TLS front door. The port is dropped, because the
// HTTPS front door is on the default port. A Host header that is not a plain
// host name is refused, so the redirect cannot be bent into a malformed or
// path-carrying Location.
func httpsRedirect() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !validRedirectHost(host) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		target := url.URL{Scheme: "https", Host: host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
		http.Redirect(w, r, target.String(), http.StatusMovedPermanently)
	})
}

// validRedirectHost accepts a DNS name or an IPv4 address: letters, digits,
// dots and hyphens only, at most 253 bytes.
func validRedirectHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	return strings.IndexFunc(h, func(c rune) bool { return !hostRune(c) }) < 0
}

// hostRune reports whether c may appear in a redirect host name.
func hostRune(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-'
}

// plainHTTP is the TLS source of the redirect listener: it never terminates TLS.
type plainHTTP struct{}

func (plainHTTP) TLSEnabled() bool                { return false }
func (plainHTTP) TLSConfig() (*tls.Config, error) { return nil, nil }
