package main

import (
	"net"
	"strings"

	"hermex/internal/config"
)

// gatewaySettings is the gateway's resolved routing configuration: the listen
// address and the backend base URLs the front door proxies to. It is derived from
// the shared config with built-in defaults, so a deployment that leaves the
// gateway_* keys unset still routes to the standard compose service names.
type gatewaySettings struct {
	addr              string
	backendMapi       string
	backendEws        string
	backendActiveSync string
	backendDav        string
	backendWebmail    string
	backendMta        string
	backendAdmin      string
	adminHost         string // lower-case host the admin panel is served under; "" serves it nowhere
}

// resolveGateway reads the gateway routing settings from the shared config,
// substituting a built-in default for every field left empty. The configuration
// file is the sole source, there is no environment override, so the resolved
// values are exactly what config.json carries (or the defaults below).
func resolveGateway(cfg *config.Config) gatewaySettings {
	return gatewaySettings{
		addr:              orDefault(cfg.GatewayAddr, ":8080"),
		backendMapi:       orDefault(cfg.GatewayBackendMapi, "http://mapi:8080"),
		backendEws:        orDefault(cfg.GatewayBackendEws, "http://ews:8080"),
		backendActiveSync: orDefault(cfg.GatewayBackendActiveSync, "http://activesync:8080"),
		backendDav:        orDefault(cfg.GatewayBackendDAV, "http://dav:8080"),
		backendWebmail:    orDefault(cfg.GatewayBackendWebmail, "http://webmail2:8080"),
		backendMta:        orDefault(cfg.GatewayBackendMta, "http://mta:8080"),
		backendAdmin:      orDefault(cfg.GatewayBackendAdmin, "https://admin:8081"),
		adminHost:         normalizeHost(cfg.AdminHostname),
	}
}

// normalizeHost reduces a host name or Host header to the form two names are
// compared in: the port dropped, lower case (RFC 4343) and without the trailing
// dot of a fully qualified name.
func normalizeHost(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// orDefault returns v when it is non-empty, otherwise def.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
