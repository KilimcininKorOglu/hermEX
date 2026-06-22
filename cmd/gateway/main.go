// Command gateway runs the hermEX single-FQDN front door: it terminates TLS and
// reverse-proxies each request to the backend daemon chosen by path prefix, so a
// client reaches autodiscover, EWS, MAPI/HTTP, RPC/HTTP, ActiveSync, DAV and
// webmail through one host.
//
// Like the other daemons it reads the shared config (-config, default
// /etc/hermex/config.json) for the database DSN, the TLS certificate
// (tls_cert/tls_key), central logging, and the hostname; only the gateway-specific
// routing is environment-driven — HERMEX_GATEWAY_ADDR sets the listen address and
// HERMEX_BACKEND_* override the backend base URLs (defaulting to the compose
// service names). TLS is served through the certificate store: an admin-uploaded
// certificate (picked up live on renewal), falling back to the config-file
// certificate — so the front door and the mail daemons present one certificate
// from one source.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/gateway"
	"hermex/internal/health"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/serve"
	"hermex/internal/tlscert"
)

// env returns the value of key when set, otherwise def.
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-gateway: %v", err)
	}

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

	addr := env("HERMEX_GATEWAY_ADDR", ":8080")
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)

	// The gateway's database connection is used only for the TLS certificate store:
	// it serves an admin-uploaded certificate — and picks up a renewal — at the front
	// door without a restart, falling back to the config-file certificate when the
	// store has none.
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-gateway: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-gateway: directory unreachable: %v", err)
	}
	provider, err := tlscert.New(cfg, directory.NewSQL(db), logger)
	if err != nil {
		log.Fatalf("hermex-gateway: tls: %v", err)
	}
	if provider.TLSEnabled() {
		go provider.RunMaintenance()
	}

	hs, err := serve.New(addr, h, provider, logger, logging.Gateway)
	if err != nil {
		log.Fatalf("hermex-gateway: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "gateway", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-gateway listening on %s", addr)
	comps := append([]lifecycle.Component{hs},
		health.Components(env("HERMEX_HEALTH_ADDR", ""), "gateway")...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, logClose, db.Close); err != nil {
		log.Fatalf("hermex-gateway: %v", err)
	}
}
