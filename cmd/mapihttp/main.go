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
	"time"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/authlimit"
	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/health"
	"hermex/internal/httplimit"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mapihttp"
	"hermex/internal/meeting"
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

	cfg, db, dir, logger, logClose := openDirectory(*cfgPath)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log
	mta.SetDefaultLogger(logger)         // post-delivery pass failures route to the central log

	// Antivirus: install the package-level scanner from clamd_addr (a no-op when
	// unset), so authenticated submissions (ROP) are scanned before relay.
	mta.EnableScanning(cfg.ClamdAddr, dir, cfg.QuarantinePath, cfg.Hostname, logger)

	spool := openSpool(cfg)
	srv := mapihttp.NewServer(dir, dir, cfg.Hostname, spool)
	// Failed-login throttle: an account that piles up failed logins is locked
	// out for the window the operator configured, so a client cannot guess
	// passwords unbounded (nor keep the daemon busy hashing them).
	loginLimiter := authlimit.New(0, 0, 0)
	srv.SetLimiter(loginLimiter)
	authlimit.Apply("hermex-mapihttp", logger, loginLimiter, dir.GetLoginLockoutSettings)
	go authlimit.RunMaintenance("hermex-mapihttp", logger, loginLimiter, dir.GetLoginLockoutSettings)
	srv.SetLogger(logger)

	// Push notifications: publish this daemon's own mailbox writes to the relay, and
	// subscribe so a parked NotificationWait/EcDoAsyncWaitEx wakes the instant a
	// change lands instead of on its cadence. Both are no-ops when notify_url is
	// empty, leaving the long-polls on their poll cadence.
	notify.EnableProducer(cfg.NotifyURL, cfg.NotifySecret, logger)
	notifyConsumer := notify.EnableConsumer(cfg.NotifyURL, cfg.NotifySecret, logger)
	srv.SetNotify(notifyConsumer)

	addr := orDefault(cfg.MapiAddr, ":8080")
	// Outbound abuse limiting: this daemon queues external mail through
	// DeliverAndRelay, so a compromised account must meet the same per-account
	// recipient cap SMTP submission enforces. It starts disabled and follows the
	// stored settings without a restart.
	mta.StartOutboundLimiter("hermex-mapihttp", logger, dir.GetOutboundSettings)
	mta.StartAutoReply("hermex-mapihttp", logger, dir.GetAutoReplySettings)
	// A meeting message this daemon delivers to a local mailbox gets the same
	// delivery-time processing the MTA applies.
	meeting.InstallDeliveryHooks(logger)
	// The operator's inbound message size limit applies to this daemon's sends
	// too: SMTP refuses an oversized message during DATA, and nothing here ever
	// reaches an SMTP session.
	mta.StartMessageSizeLimit("hermex-mapihttp", logger, dir.GetMessageSizeSettings)
	// MAPI/HTTP request-body cap: read at startup and re-read every minute so an admin's
	// change applies without a restart; 0 keeps the built-in default.
	applyMapiSizeLimit(logger, dir.GetSizeLimits, mapihttp.SetMaxRequestBody)
	go runMapiSizeMaintenance(logger, dir.GetSizeLimits, mapihttp.SetMaxRequestBody)
	// Per-client HTTP request limiter: read the stored settings at startup and
	// re-read them every minute, so an operator's change applies without a restart.
	// It is off until an operator enables it, and any read failure leaves it as it
	// is, so a settings problem never starts throttling clients.
	httpLimiter := httplimit.NewLimiter()
	httplimit.Apply("hermex-mapihttp", logger, httpLimiter, dir.GetHTTPRateLimitSettings)
	go httplimit.RunMaintenance("hermex-mapihttp", logger, httpLimiter, dir.GetHTTPRateLimitSettings)
	provider := startTLS(cfg, dir, logger)
	hs, err := serve.New(addr, srv.Handler(), provider, logger, logging.MAPI, httpLimiter)
	if err != nil {
		log.Fatalf("hermex-mapi: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "mapihttp", "addr": addr})
	log.Printf("hermex-mapi listening on %s", addr)
	runUntilSignal(cfg, db, provider, srv, hs, spool.Close, logClose)
}

// orDefault returns v, or fallback when v is empty.
func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// openDirectory loads the config, opens the directory database and builds the
// logger. Every failure here is fatal: the daemon cannot serve a mailbox without the
// accounts behind it.
func openDirectory(cfgPath string) (*config.Config, *sql.DB, *directory.SQLDirectory, *logging.Logger, func() error) {
	cfg, err := config.Load(cfgPath)
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
	// At-rest wrapping for the private keys the directory stores (DKIM signing
	// keys, uploaded TLS keys). An unset secret leaves them in plaintext and says
	// so on startup.
	dir.SetKeySecret(cfg.KeyWrapSecret())
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-mapi: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build("hermex-mapihttp", cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	return cfg, db, dir, logger, logClose
}

// openSpool opens the shared relay spool the MTA drains. External recipients of
// submitted mail are enqueued there; without it native Outlook would send local-only.
func openSpool(cfg *config.Config) *relay.Spool {
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-mapi: open relay spool: %v", err)
	}
	return spool
}

// startTLS resolves the serving certificate: the config-file cert as a fallback,
// overridden by an admin-uploaded cert the provider polls for, so a renewal applies
// without a restart.
func startTLS(cfg *config.Config, dir *directory.SQLDirectory, logger *logging.Logger) *tlscert.Provider {
	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		log.Fatalf("hermex-mapihttp: tls: %v", err)
	}
	if provider.TLSEnabled() {
		go provider.RunMaintenance()
	}
	return provider
}

// runUntilSignal starts the session maintenance and serves until a shutdown signal
// arrives, then drains the server and runs the cleanups in order.
func runUntilSignal(cfg *config.Config, db *sql.DB, provider *tlscert.Provider, srv *mapihttp.Server, hs lifecycle.Component, cleanups ...func() error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Reclaim sessions whose client vanished without Disconnect; each one otherwise
	// pins an open mailbox store for the life of the process.
	go srv.RunSessionMaintenance(ctx)
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider.TLSEnabled() {
		// Report the serving certificate's remaining validity, so a renewal that
		// failed shows as degraded before clients start failing handshakes.
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "mapi", checks...)...)
	cleanups = append(cleanups, db.Close)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, cleanups...); err != nil {
		log.Fatalf("hermex-mapi: %v", err)
	}
}

// applyMapiSizeLimit reads the stored MAPI/HTTP request-body cap and applies it. A
// missing row or a read error leaves the cap unchanged, so a settings failure never
// shrinks it unexpectedly.
func applyMapiSizeLimit(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setRequestBody func(int64)) {
	s, found, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, "hermex-mapihttp", "size-limits", "leaving the request cap unchanged", err)
		return
	}
	if !found {
		return
	}
	setRequestBody(s.MapiRequestBytes)
}

// runMapiSizeMaintenance re-applies the MAPI/HTTP request-body cap every minute so an
// admin change takes effect without a restart. It runs until the process exits.
func runMapiSizeMaintenance(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setRequestBody func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyMapiSizeLimit(logger, read, setRequestBody)
	}
}
