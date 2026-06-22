// Package gateway is the hermEX single-FQDN front door. It reverse-proxies each
// request to a backend daemon chosen by the longest matching path prefix, so a
// client (Outlook, a browser, a phone) reaches autodiscover, EWS, MAPI/HTTP,
// RPC/HTTP, ActiveSync, DAV and webmail through one host. TLS is terminated by
// the caller (serve.ListenAndServe); the gateway forwards to the backends on the
// internal network — plaintext, or HTTPS with verification skipped on that internal
// hop when the backends present self-signed certificates — and passes the
// Authorization header through for the backends to authenticate.
package gateway

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// Route maps a path prefix (matched case-insensitively) to a backend base URL.
// The prefix "/" is the catch-all default.
type Route struct {
	Prefix string
	Target string // backend base URL, e.g. http://mapi:8080
}

// Handler builds the reverse-proxy front door. Each request is routed to the
// backend whose prefix is the longest case-insensitive match for the request
// path; an unmatched path (no "/" default supplied) yields 502. It errors if a
// target URL cannot be parsed or no routes are given.
func Handler(routes []Route) (http.Handler, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("gateway: no routes")
	}
	type compiled struct {
		prefix string
		proxy  *httputil.ReverseProxy
	}
	compiledRoutes := make([]compiled, 0, len(routes))
	// Backends share one transport. When TLS is terminated end-to-end they present
	// self-signed certificates on the internal network, so verification is skipped on
	// the gateway→backend hop (the external hop to the client stays verified).
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	for _, r := range routes {
		u, err := url.Parse(r.Target)
		if err != nil {
			return nil, fmt.Errorf("gateway: target %q: %w", r.Target, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("gateway: target %q must be an absolute URL", r.Target)
		}
		proxy := httputil.NewSingleHostReverseProxy(u)
		proxy.Transport = transport
		compiledRoutes = append(compiledRoutes, compiled{strings.ToLower(r.Prefix), proxy})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := strings.ToLower(req.URL.Path)
		best, bestLen := -1, -1
		for i, c := range compiledRoutes {
			if strings.HasPrefix(path, c.prefix) && len(c.prefix) > bestLen {
				best, bestLen = i, len(c.prefix)
			}
		}
		if best < 0 {
			http.Error(w, "no backend for path", http.StatusBadGateway)
			return
		}
		compiledRoutes[best].proxy.ServeHTTP(w, req)
	}), nil
}
