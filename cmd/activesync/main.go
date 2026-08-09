// Command activesync runs the hermEX Exchange ActiveSync (EAS) HTTP server: it
// authenticates users against the directory database with HTTP Basic and serves
// the MS-ASHTTP endpoint plus mobilesync Autodiscover, syncing their mailbox to
// phones over MS-ASCMD/MS-ASWBXML.
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

	"hermex/internal/activesync"
	"hermex/internal/authlimit"
	"hermex/internal/config"
	"hermex/internal/directory"
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

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-activesync: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-activesync: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-activesync: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-activesync: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build("hermex-activesync", cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log
	mta.SetDefaultLogger(logger)         // post-delivery pass failures route to the central log

	// Antivirus: install the package-level scanner from clamd_addr (a no-op when
	// unset), so authenticated submissions are scanned before relay.
	mta.EnableScanning(cfg.ClamdAddr, dir, cfg.QuarantinePath, cfg.Hostname, logger)

	// Push notifications: publish this daemon's own mailbox writes, and subscribe so
	// a held Ping wakes the instant a change lands instead of on its cadence. No-ops
	// when notify_url is empty.
	notify.EnableProducer(cfg.NotifyURL, cfg.NotifySecret, logger)

	srv := activesync.NewServer(dir, dir, cfg.Hostname)
	srv.Logger = logger
	// Failed-login throttle: an account that piles up failed logins is locked
	// out for the window the operator configured, so a client cannot guess
	// passwords unbounded (nor keep the daemon busy hashing them).
	srv.Limiter = authlimit.New(0, 0, 0)
	authlimit.Apply("hermex-activesync", srv.Limiter, dir.GetLoginLockoutSettings)
	go authlimit.RunMaintenance("hermex-activesync", srv.Limiter, dir.GetLoginLockoutSettings)
	srv.SetNotify(notify.EnableConsumer(cfg.NotifyURL, cfg.NotifySecret, logger))
	// Enqueue external recipients of SendMail into the shared relay spool the MTA
	// drains; without it ActiveSync would send local-only.
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-activesync: open relay spool: %v", err)
	}
	srv.Spool = spool
	// Record live-session telemetry for the admin mobile-devices monitor.
	srv.Sessions = dir
	// ActiveSync request-body cap: read at startup and re-read every minute so an
	// admin's change applies without a restart; 0 keeps the built-in default.
	applyActiveSyncSizeLimit(dir.GetSizeLimits, activesync.SetMaxRequestBody)
	go runActiveSyncSizeMaintenance(dir.GetSizeLimits, activesync.SetMaxRequestBody)
	addr := cfg.ActiveSyncAddr
	if addr == "" {
		addr = ":8080"
	}
	// Outbound abuse limiting: this daemon queues external mail through
	// DeliverAndRelay, so a compromised account must meet the same per-account
	// recipient cap SMTP submission enforces. It starts disabled and follows the
	// stored settings without a restart.
	mta.StartOutboundLimiter("hermex-activesync", logger, dir.GetOutboundSettings)
	// The operator's inbound message size limit applies to this daemon's sends
	// too: SMTP refuses an oversized message during DATA, and nothing here ever
	// reaches an SMTP session.
	mta.StartMessageSizeLimit("hermex-activesync", dir.GetMessageSizeSettings)
	// Per-client HTTP request limiter: read the stored settings at startup and
	// re-read them every minute, so an operator's change applies without a restart.
	// It is off until an operator enables it, and any read failure leaves it as it
	// is, so a settings problem never starts throttling clients.
	httpLimiter := httplimit.NewLimiter()
	httplimit.Apply("hermex-activesync", httpLimiter, dir.GetHTTPRateLimitSettings)
	go httplimit.RunMaintenance("hermex-activesync", httpLimiter, dir.GetHTTPRateLimitSettings)
	// TLS certificates come from the provider: the config-file cert as a fallback,
	// overridden by an admin-uploaded cert the provider polls for, so a renewal
	// applies without a restart.
	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		log.Fatalf("hermex-activesync: tls: %v", err)
	}
	if provider.TLSEnabled() {
		go provider.RunMaintenance()
	}
	hs, err := serve.New(addr, srv.Handler(), provider, logger, logging.ActiveSync, httpLimiter)
	if err != nil {
		log.Fatalf("hermex-activesync: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "activesync", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go purgeSessionsLoop(ctx, dir, logger)
	log.Printf("hermex-activesync listening on %s", addr)
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider.TLSEnabled() {
		// Report the serving certificate's remaining validity, so a renewal that
		// failed shows as degraded before clients start failing handshakes.
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "activesync", checks...)...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, spool.Close, logClose, db.Close); err != nil {
		log.Fatalf("hermex-activesync: %v", err)
	}
}

// applyActiveSyncSizeLimit reads the stored ActiveSync request-body cap and applies it.
// A missing row or a read error leaves the cap unchanged, so a settings failure never
// shrinks it unexpectedly.
func applyActiveSyncSizeLimit(read func() (directory.SizeLimits, bool, error), setRequestBody func(int64)) {
	s, found, err := read()
	if err != nil {
		log.Printf("hermex-activesync: size limits read failed, leaving the request cap unchanged: %v", err)
		return
	}
	if !found {
		return
	}
	setRequestBody(s.ActiveSyncRequestBytes)
}

// runActiveSyncSizeMaintenance re-applies the ActiveSync request-body cap every minute
// so an admin change takes effect without a restart. It runs until the process exits.
func runActiveSyncSizeMaintenance(read func() (directory.SizeLimits, bool, error), setRequestBody func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyActiveSyncSizeLimit(read, setRequestBody)
	}
}

// purgeSessionsLoop sweeps aged live-session telemetry rows once a minute until
// the daemon shuts down, keeping the active_sessions table from growing without
// bound. The read path already hides stale rows by age, so a missed sweep is
// harmless, failures are logged, not fatal.
func purgeSessionsLoop(ctx context.Context, dir *directory.SQLDirectory, logger *logging.Logger) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := dir.PurgeStaleSessions(time.Now().Unix()); err != nil {
				logger.Info(logging.ActiveSync, "session.purge.fail", logging.Fields{"error": err.Error()})
			}
		}
	}
}
