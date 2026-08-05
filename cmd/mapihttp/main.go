// Command mapihttp runs the hermEX native Outlook transport server (MAPI/HTTP,
// [MS-OXCMAPIHTTP]): it authenticates users against the directory database with
// HTTP Basic and serves the EMSMDB (/mapi/emsmdb) and NSPI (/mapi/nspi)
// endpoints.
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
	"hermex/internal/health"
	"hermex/internal/httplimit"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mapihttp"
	"hermex/internal/mta"
	"hermex/internal/notify"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
	"hermex/internal/serve"
	"hermex/internal/tlscert"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-mapi: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-mapi: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-mapi: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-mapi: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	// Antivirus: install the package-level scanner from clamd_addr (a no-op when
	// unset), so authenticated submissions (ROP) are scanned before relay.
	mta.EnableScanning(cfg.ClamdAddr, dir, cfg.QuarantinePath, cfg.Hostname, logger)

	// Enqueue external recipients of submitted mail into the shared relay spool the
	// MTA drains; without it native Outlook would send local-only.
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-mapi: open relay spool: %v", err)
	}
	srv := mapihttp.NewServer(dir, dir, cfg.Hostname, spool)
	srv.Logger = logger

	// Push notifications: publish this daemon's own mailbox writes to the relay, and
	// subscribe so a parked NotificationWait/EcDoAsyncWaitEx wakes the instant a
	// change lands instead of on its cadence. Both are no-ops when notify_url is
	// empty, leaving the long-polls on their poll cadence.
	notify.EnableProducer(cfg.NotifyURL, cfg.NotifySecret, logger)
	notifyConsumer := notify.EnableConsumer(cfg.NotifyURL, cfg.NotifySecret, logger)
	srv.SetNotify(notifyConsumer)

	addr := cfg.MapiAddr
	if addr == "" {
		addr = ":8080"
	}
	// Per-client HTTP request limiter: read the stored settings at startup and
	// re-read them every minute, so an operator's change applies without a restart.
	// It is off until an operator enables it, and any read failure leaves it as it
	// is, so a settings problem never starts throttling clients.
	httpLimiter := httplimit.NewLimiter()
	httplimit.Apply("hermex-mapihttp", httpLimiter, dir.GetHTTPRateLimitSettings)
	go httplimit.RunMaintenance("hermex-mapihttp", httpLimiter, dir.GetHTTPRateLimitSettings)
	// TLS certificates come from the provider: the config-file cert as a fallback,
	// overridden by an admin-uploaded cert the provider polls for, so a renewal
	// applies without a restart.
	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		log.Fatalf("hermex-mapihttp: tls: %v", err)
	}
	if provider.TLSEnabled() {
		go provider.RunMaintenance()
	}
	hs, err := serve.New(addr, srv.Handler(), provider, logger, logging.MAPI, httpLimiter)
	if err != nil {
		log.Fatalf("hermex-mapi: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "mapihttp", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-mapi listening on %s", addr)
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider.TLSEnabled() {
		// Report the serving certificate's remaining validity, so a renewal that
		// failed shows as degraded before clients start failing handshakes.
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "mapi", checks...)...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, spool.Close, logClose, db.Close); err != nil {
		log.Fatalf("hermex-mapi: %v", err)
	}
}
