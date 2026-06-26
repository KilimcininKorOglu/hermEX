package main

import "hermex/internal/config"

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
}

// resolveGateway reads the gateway routing settings from the shared config,
// substituting a built-in default for every field left empty. The configuration
// file is the sole source — there is no environment override — so the resolved
// values are exactly what config.json carries (or the defaults below).
func resolveGateway(cfg *config.Config) gatewaySettings {
	return gatewaySettings{
		addr:              orDefault(cfg.GatewayAddr, ":8080"),
		backendMapi:       orDefault(cfg.GatewayBackendMapi, "http://mapi:8080"),
		backendEws:        orDefault(cfg.GatewayBackendEws, "http://ews:8080"),
		backendActiveSync: orDefault(cfg.GatewayBackendActiveSync, "http://activesync:8080"),
		backendDav:        orDefault(cfg.GatewayBackendDAV, "http://dav:8080"),
		backendWebmail:    orDefault(cfg.GatewayBackendWebmail, "http://webmail2:8080"),
	}
}

// orDefault returns v when it is non-empty, otherwise def.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
