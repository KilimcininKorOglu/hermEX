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

	cfg, err := config.Load(*cfgPath)
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
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-dav: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build("hermex-dav", cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	srv := dav.NewServer(dir, dir, cfg.Hostname)
	srv.Logger = logger // implicit-scheduling delivery failures route to the central log
	// Scheduling-Outbox iTIP messages with external recipients are enqueued into the
	// shared relay spool the MTA drains, DKIM-signed with the sending domain's key as
	// they are spooled (RFC 6638 §5).
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-dav: open relay spool: %v", err)
	}
	spool.Signer = &dkimsign.Signer{Keys: dir, Logger: logger}
	srv.SetSpool(spool)
	// Wire the push subscription transport to the central wake bus so a change in any
	// daemon wakes a subscribed DAV client sub-second (calendarserver-push). A nil
	// consumer (no notify_url) degrades the push long-poll to its cadence floor.
	srv.SetNotify(notify.EnableConsumer(cfg.NotifyURL, cfg.NotifySecret, logger))
	// CalDAV/CardDAV PUT body caps: read at startup and re-read every minute so an
	// admin's change applies without a restart; 0 keeps the built-in defaults.
	applyDAVSizeLimits(dir.GetSizeLimits, srv.SetMaxICal, srv.SetMaxVCard)
	go runDAVSizeMaintenance(dir.GetSizeLimits, srv.SetMaxICal, srv.SetMaxVCard)
	addr := cfg.DAVAddr
	if addr == "" {
		addr = ":8080"
	}
	// Outbound abuse limiting: this daemon queues external mail through
	// DeliverAndRelay, so a compromised account must meet the same per-account
	// recipient cap SMTP submission enforces. It starts disabled and follows the
	// stored settings without a restart.
	mta.StartOutboundLimiter("hermex-dav", logger, dir.GetOutboundSettings)
	// The operator's inbound message size limit applies to this daemon's sends
	// too: SMTP refuses an oversized message during DATA, and nothing here ever
	// reaches an SMTP session.
	mta.StartMessageSizeLimit("hermex-dav", dir.GetMessageSizeSettings)
	// Per-client HTTP request limiter: read the stored settings at startup and
	// re-read them every minute, so an operator's change applies without a restart.
	// It is off until an operator enables it, and any read failure leaves it as it
	// is, so a settings problem never starts throttling clients.
	httpLimiter := httplimit.NewLimiter()
	httplimit.Apply("hermex-dav", httpLimiter, dir.GetHTTPRateLimitSettings)
	go httplimit.RunMaintenance("hermex-dav", httpLimiter, dir.GetHTTPRateLimitSettings)
	// TLS certificates come from the provider: the config-file cert as a fallback,
	// overridden by an admin-uploaded cert the provider polls for, so a renewal
	// applies without a restart.
	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		log.Fatalf("hermex-dav: tls: %v", err)
	}
	if provider.TLSEnabled() {
		go provider.RunMaintenance()
	}
	hs, err := serve.New(addr, srv.Handler(), provider, logger, logging.DAV, httpLimiter)
	if err != nil {
		log.Fatalf("hermex-dav: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "dav", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-dav listening on %s", addr)
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider.TLSEnabled() {
		// Report the serving certificate's remaining validity, so a renewal that
		// failed shows as degraded before clients start failing handshakes.
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "dav", checks...)...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, spool.Close, logClose, db.Close); err != nil {
		log.Fatalf("hermex-dav: %v", err)
	}
}

// applyDAVSizeLimits reads the stored CalDAV/CardDAV PUT body caps and applies them to
// the server. A missing row or a read error leaves the caps unchanged, so a settings
// failure never shrinks them unexpectedly.
func applyDAVSizeLimits(read func() (directory.SizeLimits, bool, error), setICal, setVCard func(int64)) {
	s, found, err := read()
	if err != nil {
		log.Printf("hermex-dav: size limits read failed, leaving the body caps unchanged: %v", err)
		return
	}
	if !found {
		return
	}
	setICal(s.DAVICalBytes)
	setVCard(s.DAVVCardBytes)
}

// runDAVSizeMaintenance re-applies the DAV PUT body caps every minute so an admin
// change takes effect without a restart. It runs until the process exits.
func runDAVSizeMaintenance(read func() (directory.SizeLimits, bool, error), setICal, setVCard func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyDAVSizeLimits(read, setICal, setVCard)
	}
}
