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
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-ews: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	// Antivirus: install the package-level scanner from clamd_addr (a no-op when
	// unset), so authenticated submissions are scanned before relay.
	mta.EnableScanning(cfg.ClamdAddr, dir, cfg.QuarantinePath, cfg.Hostname, logger)

	// Push notifications: publish this daemon's own mailbox writes, and subscribe so
	// a held GetStreamingEvents emits a continuation the instant a watched mailbox
	// changes instead of on its interval. No-ops when notify_url is empty.
	notify.EnableProducer(cfg.NotifyURL, cfg.NotifySecret, logger)

	srv := ews.NewServer(dir, dir, cfg.Hostname)
	srv.Logger = logger
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
	applyEWSSizeLimit(dir.GetSizeLimits, ews.SetMaxRequestBody)
	go runEWSSizeMaintenance(dir.GetSizeLimits, ews.SetMaxRequestBody)
	addr := cfg.EWSAddr
	if addr == "" {
		addr = ":8080"
	}
	// Per-client HTTP request limiter: read the stored settings at startup and
	// re-read them every minute, so an operator's change applies without a restart.
	// It is off until an operator enables it, and any read failure leaves it as it
	// is, so a settings problem never starts throttling clients.
	httpLimiter := httplimit.NewLimiter()
	httplimit.Apply("hermex-ews", httpLimiter, dir.GetHTTPRateLimitSettings)
	go httplimit.RunMaintenance("hermex-ews", httpLimiter, dir.GetHTTPRateLimitSettings)
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
func applyEWSSizeLimit(read func() (directory.SizeLimits, bool, error), setRequestBody func(int64)) {
	s, found, err := read()
	if err != nil {
		log.Printf("hermex-ews: size limits read failed, leaving the request cap unchanged: %v", err)
		return
	}
	if !found {
		return
	}
	setRequestBody(s.EWSRequestBytes)
}

// runEWSSizeMaintenance re-applies the EWS request-body cap every minute so an admin
// change takes effect without a restart. It runs until the process exits.
func runEWSSizeMaintenance(read func() (directory.SizeLimits, bool, error), setRequestBody func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyEWSSizeLimit(read, setRequestBody)
	}
}
