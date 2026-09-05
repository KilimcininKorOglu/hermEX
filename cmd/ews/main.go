// Command ews runs the hermEX Exchange Web Services (EWS) HTTP server: it
// authenticates users against the directory database with HTTP Basic and serves
// their mailbox over SOAP on /EWS/Exchange.asmx.
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
	"hermex/internal/ews"
	"hermex/internal/health"
	"hermex/internal/httplimit"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mta"
	"hermex/internal/notify"
	"hermex/internal/objectstore"
	"hermex/internal/publicfolder"
	"hermex/internal/relay"
	"hermex/internal/serve"
	"hermex/internal/tlscert"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-ews: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-ews: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-ews: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	// At-rest wrapping for the private keys the directory stores (DKIM signing
	// keys, uploaded TLS keys). An unset secret leaves them in plaintext and says
	// so on startup.
	dir.SetKeySecret(cfg.KeyWrapSecret())
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-ews: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build("hermex-ews", cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log
	mta.SetDefaultLogger(logger)         // post-delivery pass failures route to the central log

	// Antivirus: install the package-level scanner from clamd_addr (a no-op when
	// unset), so authenticated submissions are scanned before relay.
	mta.EnableScanning(cfg.ClamdAddr, dir, cfg.QuarantinePath, cfg.Hostname, logger)

	// Push notifications: publish this daemon's own mailbox writes, and subscribe so
	// a held GetStreamingEvents emits a continuation the instant a watched mailbox
	// changes instead of on its interval. No-ops when notify_url is empty.
	notify.EnableProducer(cfg.NotifyURL, cfg.NotifySecret, logger)

	srv := ews.NewServer(dir, dir, cfg.Hostname)
	srv.Logger = logger
	// Failed-login throttle: an account that piles up failed logins is locked
	// out for the window the operator configured, so a client cannot guess
	// passwords unbounded (nor keep the daemon busy hashing them).
	srv.Limiter = authlimit.New(0, 0, 0)
	authlimit.Apply("hermex-ews", logger, srv.Limiter, dir.GetLoginLockoutSettings)
	go authlimit.RunMaintenance("hermex-ews", logger, srv.Limiter, dir.GetLoginLockoutSettings)
	srv.SetNotify(notify.EnableConsumer(cfg.NotifyURL, cfg.NotifySecret, logger))
	srv.Pub = publicfolder.New(cfg) // per-domain public folders rooted at HomedirFor
	// Enqueue external recipients of sent items into the shared relay spool the
	// MTA drains; without it EWS would send local-only.
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-ews: open relay spool: %v", err)
	}
	srv.Spool = spool
	// EWS SOAP request-body cap: read at startup and re-read every minute so an admin's
	// change applies without a restart; 0 keeps the built-in default.
	applyEWSSizeLimit(logger, dir.GetSizeLimits, ews.SetMaxRequestBody, ews.SetMaxFreeBusyTargets)
	go runEWSSizeMaintenance(logger, dir.GetSizeLimits, ews.SetMaxRequestBody, ews.SetMaxFreeBusyTargets)
	addr := cfg.EWSAddr
	if addr == "" {
		addr = ":8080"
	}
	// Outbound abuse limiting: this daemon queues external mail through
	// DeliverAndRelay, so a compromised account must meet the same per-account
	// recipient cap SMTP submission enforces. It starts disabled and follows the
	// stored settings without a restart.
	mta.StartOutboundLimiter("hermex-ews", logger, dir.GetOutboundSettings)
	mta.StartAutoReply("hermex-ews", logger, dir.GetAutoReplySettings)
	// The operator's inbound message size limit applies to this daemon's sends
	// too: SMTP refuses an oversized message during DATA, and nothing here ever
	// reaches an SMTP session.
	mta.StartMessageSizeLimit("hermex-ews", logger, dir.GetMessageSizeSettings)
	// Per-client HTTP request limiter: read the stored settings at startup and
	// re-read them every minute, so an operator's change applies without a restart.
	// It is off until an operator enables it, and any read failure leaves it as it
	// is, so a settings problem never starts throttling clients.
	httpLimiter := httplimit.NewLimiter()
	httplimit.Apply("hermex-ews", logger, httpLimiter, dir.GetHTTPRateLimitSettings)
	go httplimit.RunMaintenance("hermex-ews", logger, httpLimiter, dir.GetHTTPRateLimitSettings)
	// TLS certificates come from the provider: the config-file cert as a fallback,
	// overridden by an admin-uploaded cert the provider polls for, so a renewal
	// applies without a restart.
	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		log.Fatalf("hermex-ews: tls: %v", err)
	}
	if provider.TLSEnabled() {
		go provider.RunMaintenance()
	}
	hs, err := serve.New(addr, srv.Handler(), provider, logger, logging.EWS, httpLimiter)
	if err != nil {
		log.Fatalf("hermex-ews: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "ews", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-ews listening on %s", addr)
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider.TLSEnabled() {
		// Report the serving certificate's remaining validity, so a renewal that
		// failed shows as degraded before clients start failing handshakes.
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "ews", checks...)...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, spool.Close, logClose, db.Close); err != nil {
		log.Fatalf("hermex-ews: %v", err)
	}
}

// applyEWSSizeLimit reads the stored EWS request-body cap and applies it. A missing row
// or a read error leaves the cap unchanged, so a settings failure never shrinks it
// unexpectedly.
func applyEWSSizeLimit(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setRequestBody, setFreeBusyTargets func(int64)) {
	s, found, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, "hermex-ews", "size-limits", "leaving the request cap unchanged", err)
		return
	}
	if !found {
		return
	}
	setRequestBody(s.EWSRequestBytes)
	setFreeBusyTargets(s.FreeBusyMaxTargets)
}

// runEWSSizeMaintenance re-applies the EWS request-body cap every minute so an admin
// change takes effect without a restart. It runs until the process exits.
func runEWSSizeMaintenance(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setRequestBody, setFreeBusyTargets func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyEWSSizeLimit(logger, read, setRequestBody, setFreeBusyTargets)
	}
}
