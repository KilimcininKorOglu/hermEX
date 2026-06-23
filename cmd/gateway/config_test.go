package main

import (
	"testing"

	"hermex/internal/config"
)

// TestResolveGatewayDefaults proves an empty config yields the standard compose
// service routing, so a deployment that omits the gateway_* keys still reaches every
// backend. The values are load-bearing: a wrong default target would silently
// misroute requests, so the test pins each one rather than merely that a string is
// non-empty.
func TestResolveGatewayDefaults(t *testing.T) {
	gw := resolveGateway(&config.Config{})
	want := gatewaySettings{
		addr:              ":8080",
		backendMapi:       "http://mapi:8080",
		backendEws:        "http://ews:8080",
		backendActiveSync: "http://activesync:8080",
		backendDav:        "http://dav:8080",
		backendWebmail:    "http://webmail:8080",
	}
	if gw != want {
		t.Errorf("resolveGateway(empty) = %+v, want %+v", gw, want)
	}
}

// TestResolveGatewayFromConfig proves the config values override the defaults and
// that the source is the config file alone (no environment is consulted): each field
// set in config.json is the one used. This guards the env→json migration — if a field
// silently fell back to a default or an env value, routing would diverge from what the
// operator configured.
func TestResolveGatewayFromConfig(t *testing.T) {
	cfg := &config.Config{
		GatewayAddr:              ":443",
		GatewayBackendMapi:       "https://mapi:8080",
		GatewayBackendEws:        "https://ews:8080",
		GatewayBackendActiveSync: "https://activesync:8080",
		GatewayBackendDAV:        "https://dav:8080",
		GatewayBackendWebmail:    "https://webmail:8080",
	}
	gw := resolveGateway(cfg)
	want := gatewaySettings{
		addr:              ":443",
		backendMapi:       "https://mapi:8080",
		backendEws:        "https://ews:8080",
		backendActiveSync: "https://activesync:8080",
		backendDav:        "https://dav:8080",
		backendWebmail:    "https://webmail:8080",
	}
	if gw != want {
		t.Errorf("resolveGateway(configured) = %+v, want %+v", gw, want)
	}
}

// TestResolveGatewayPartialOverride proves a single configured field is honored while
// the rest still default — the per-field fallback is independent, not all-or-nothing.
func TestResolveGatewayPartialOverride(t *testing.T) {
	gw := resolveGateway(&config.Config{GatewayBackendWebmail: "https://webmail:8080"})
	if gw.backendWebmail != "https://webmail:8080" {
		t.Errorf("backendWebmail = %q, want the configured value", gw.backendWebmail)
	}
	if gw.backendMapi != "http://mapi:8080" {
		t.Errorf("backendMapi = %q, want the default when unset", gw.backendMapi)
	}
}
