// Command gateway runs the hermEX single-FQDN front door: it terminates TLS and
// reverse-proxies each request to the backend daemon chosen by path prefix, so a
// client reaches autodiscover, EWS, MAPI/HTTP, RPC/HTTP, ActiveSync, DAV and
// webmail through one host.
//
// The gateway is pure infrastructure: it touches no database or mailbox store,
// so it is configured entirely from the environment rather than the shared mail
// config (which requires a DSN it would never use). HERMEX_GATEWAY_ADDR sets the
// listen address; HERMEX_TLS_CERT/HERMEX_TLS_KEY enable TLS (absent => plaintext,
// for terminating TLS at a separate proxy); HERMEX_BACKEND_* override the backend
// base URLs, which default to the compose service names.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"hermex/internal/config"
	"hermex/internal/gateway"
	"hermex/internal/lifecycle"
	"hermex/internal/serve"
)

// env returns the value of key when set, otherwise def.
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	mapi := env("HERMEX_BACKEND_MAPI", "http://mapi:8080")
	ews := env("HERMEX_BACKEND_EWS", "http://ews:8080")
	activesync := env("HERMEX_BACKEND_ACTIVESYNC", "http://activesync:8080")
	dav := env("HERMEX_BACKEND_DAV", "http://dav:8080")
	webmail := env("HERMEX_BACKEND_WEBMAIL", "http://webmail:8080")

	// Both EWS and ActiveSync serve /autodiscover/autodiscover.xml; the gateway
	// routes it to EWS for the Outlook-desktop settings. Mobile (ActiveSync)
	// autodiscover via the gateway would need request-body inspection and is not
	// wired here.
	h, err := gateway.Handler([]gateway.Route{
		{Prefix: "/mapi/", Target: mapi},
		{Prefix: "/rpc/", Target: mapi},
		{Prefix: "/rpcwithcert/", Target: mapi},
		{Prefix: "/ews/", Target: ews},
		{Prefix: "/autodiscover/", Target: ews},
		{Prefix: "/microsoft-server-activesync", Target: activesync},
		{Prefix: "/.well-known/carddav", Target: dav},
		{Prefix: "/.well-known/caldav", Target: dav},
		{Prefix: "/dav/", Target: dav},
		{Prefix: "/", Target: webmail},
	})
	if err != nil {
		log.Fatalf("hermex-gateway: %v", err)
	}

	// serve.New terminates TLS when the (cert,key) pair is set and serves
	// plaintext otherwise; the gateway needs only those fields, so a minimal
	// config is constructed rather than loaded.
	cfg := &config.Config{TLSCert: os.Getenv("HERMEX_TLS_CERT"), TLSKey: os.Getenv("HERMEX_TLS_KEY")}
	addr := env("HERMEX_GATEWAY_ADDR", ":8080")
	hs, err := serve.New(addr, h, cfg)
	if err != nil {
		log.Fatalf("hermex-gateway: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-gateway listening on %s", addr)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, []lifecycle.Component{hs}); err != nil {
		log.Fatalf("hermex-gateway: %v", err)
	}
}
