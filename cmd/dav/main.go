// Command dav runs the hermEX CalDAV/CardDAV HTTP server: it authenticates users
// against the directory database with HTTP Basic and serves their contacts (and,
// later, calendars) from the store.
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
	"hermex/internal/dav"
	"hermex/internal/directory"
	"hermex/internal/dkimsign"
	"hermex/internal/health"
	"hermex/internal/httplimit"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
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

	srv := dav.NewServer(dir, dir, cfg.Hostname)
	srv.Logger = logger // implicit-scheduling delivery failures route to the central log
	// Failed-login throttle: an account that piles up failed logins is locked
	// out for the window the operator configured, so a client cannot guess
	// passwords unbounded (nor keep the daemon busy hashing them).
	srv.Limiter = authlimit.New(0, 0, 0)
	authlimit.Apply("hermex-dav", logger, srv.Limiter, dir.GetLoginLockoutSettings)
	go authlimit.RunMaintenance("hermex-dav", logger, srv.Limiter, dir.GetLoginLockoutSettings)
	spool := openSpool(cfg, dir, logger)
	srv.SetSpool(spool)
	// Wire the push subscription transport to the central wake bus so a change in any
	// daemon wakes a subscribed DAV client sub-second (calendarserver-push). A nil
	// consumer (no notify_url) degrades the push long-poll to its cadence floor.
	srv.SetNotify(notify.EnableConsumer(cfg.NotifyURL, cfg.NotifySecret, logger))
	// CalDAV/CardDAV PUT body caps: read at startup and re-read every minute so an
	// admin's change applies without a restart; 0 keeps the built-in defaults.
	applyDAVSizeLimits(logger, dir.GetSizeLimits, srv.SetMaxICal, srv.SetMaxVCard, dav.SetMaxFreeBusyTargets)
	go runDAVSizeMaintenance(logger, dir.GetSizeLimits, srv.SetMaxICal, srv.SetMaxVCard, dav.SetMaxFreeBusyTargets)
	addr := orDefault(cfg.DAVAddr, ":8080")
	// Outbound abuse limiting: this daemon queues external mail through
	// DeliverAndRelay, so a compromised account must meet the same per-account
	// recipient cap SMTP submission enforces. It starts disabled and follows the
	// stored settings without a restart.
	mta.StartOutboundLimiter("hermex-dav", logger, dir.GetOutboundSettings)
	mta.StartAutoReply("hermex-dav", logger, dir.GetAutoReplySettings)
	// The operator's inbound message size limit applies to this daemon's sends
	// too: SMTP refuses an oversized message during DATA, and nothing here ever
	// reaches an SMTP session.
	mta.StartMessageSizeLimit("hermex-dav", logger, dir.GetMessageSizeSettings)
	// Per-client HTTP request limiter: read the stored settings at startup and
	// re-read them every minute, so an operator's change applies without a restart.
	// It is off until an operator enables it, and any read failure leaves it as it
	// is, so a settings problem never starts throttling clients.
	httpLimiter := httplimit.NewLimiter()
	httplimit.Apply("hermex-dav", logger, httpLimiter, dir.GetHTTPRateLimitSettings)
	go httplimit.RunMaintenance("hermex-dav", logger, httpLimiter, dir.GetHTTPRateLimitSettings)
	provider := startTLS(cfg, dir, logger)
	hs, err := serve.New(addr, srv.Handler(), provider, logger, logging.DAV, httpLimiter)
	if err != nil {
		log.Fatalf("hermex-dav: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "dav", "addr": addr})
	log.Printf("hermex-dav listening on %s", addr)
	runUntilSignal(cfg, db, provider, hs, spool.Close, logClose)
}

// orDefault returns v, or fallback when v is empty.
func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// openDirectory loads the config, opens the directory database and builds the
// logger. Every failure here is fatal: the daemon cannot serve a collection without
// the accounts behind it.
func openDirectory(cfgPath string) (*config.Config, *sql.DB, *directory.SQLDirectory, *logging.Logger, func() error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("hermex-dav: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-dav: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-dav: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	// At-rest wrapping for the private keys the directory stores (DKIM signing
	// keys, uploaded TLS keys). An unset secret leaves them in plaintext and says
	// so on startup.
	dir.SetKeySecret(cfg.KeyWrapSecret())
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-dav: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build("hermex-dav", cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	return cfg, db, dir, logger, logClose
}

// openSpool opens the shared relay spool the MTA drains. Scheduling-Outbox iTIP
// messages with external recipients are enqueued there, DKIM-signed with the sending
// domain's key as they are spooled (RFC 6638 §5).
func openSpool(cfg *config.Config, dir *directory.SQLDirectory, logger *logging.Logger) *relay.Spool {
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-dav: open relay spool: %v", err)
	}
	spool.Signer = &dkimsign.Signer{Keys: dir, Logger: logger}
	return spool
}

// startTLS resolves the serving certificate: the config-file cert as a fallback,
// overridden by an admin-uploaded cert the provider polls for, so a renewal applies
// without a restart.
func startTLS(cfg *config.Config, dir *directory.SQLDirectory, logger *logging.Logger) *tlscert.Provider {
	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		log.Fatalf("hermex-dav: tls: %v", err)
	}
	if provider.TLSEnabled() {
		go provider.RunMaintenance()
	}
	return provider
}

// runUntilSignal serves until a shutdown signal arrives, then drains the server and
// runs the cleanups in order.
func runUntilSignal(cfg *config.Config, db *sql.DB, provider *tlscert.Provider, hs lifecycle.Component, cleanups ...func() error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider.TLSEnabled() {
		// Report the serving certificate's remaining validity, so a renewal that
		// failed shows as degraded before clients start failing handshakes.
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "dav", checks...)...)
	cleanups = append(cleanups, db.Close)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, cleanups...); err != nil {
		log.Fatalf("hermex-dav: %v", err)
	}
}

// applyDAVSizeLimits reads the stored CalDAV/CardDAV PUT body caps and applies them to
// the server. A missing row or a read error leaves the caps unchanged, so a settings
// failure never shrinks them unexpectedly.
func applyDAVSizeLimits(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setICal, setVCard, setFreeBusyTargets func(int64)) {
	s, found, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, "hermex-dav", "size-limits", "leaving the body caps unchanged", err)
		return
	}
	if !found {
		return
	}
	setICal(s.DAVICalBytes)
	setVCard(s.DAVVCardBytes)
	setFreeBusyTargets(s.FreeBusyMaxTargets)
}

// runDAVSizeMaintenance re-applies the DAV PUT body caps every minute so an admin
// change takes effect without a restart. It runs until the process exits.
func runDAVSizeMaintenance(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setICal, setVCard, setFreeBusyTargets func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyDAVSizeLimits(logger, read, setICal, setVCard, setFreeBusyTargets)
	}
}
