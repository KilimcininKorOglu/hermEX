// Command webmail2 runs the hermEX webmail2 server: it serves the single-page
// app and its /api/v1 JSON API, authenticating users against the directory
// database and serving their mailboxes from the object store.
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
	"hermex/internal/dkimsign"
	"hermex/internal/health"
	"hermex/internal/httplimit"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/publicfolder"
	"hermex/internal/relay"
	"hermex/internal/serve"
	"hermex/internal/tlscert"
	"hermex/internal/webmail2api"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-webmail2: %v", err)
	}
	if cfg.Webmail2Secret == "" {
		log.Fatalf("hermex-webmail2: webmail2_secret is required")
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-webmail2: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-webmail2: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-webmail2: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build("hermex-webmail2", cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log
	mta.SetDefaultLogger(logger)         // post-delivery pass failures route to the central log
	webmail2api.SetDefaultLogger(logger) // best-effort API failures (a Sent copy that could not be filed) route there too

	// Antivirus: install the package-level scanner from clamd_addr (a no-op when
	// unset), so authenticated submissions are scanned before relay.
	mta.EnableScanning(cfg.ClamdAddr, dir, cfg.QuarantinePath, cfg.Hostname, logger)

	// Composed mail with external recipients is enqueued into the shared relay spool
	// the MTA drains, DKIM-signed with the sending domain's key as it is spooled.
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-webmail2: open relay spool: %v", err)
	}
	spool.Signer = &dkimsign.Signer{Keys: dir, Logger: logger}

	// The session cookie is marked Secure when the front door terminates TLS, which
	// the shared config signals via a configured certificate.
	api := webmail2api.NewServer(dir, dir, spool, cfg.Hostname, []byte(cfg.Webmail2Secret), cfg.Webmail2Dist, cfg.TLSCert != "")
	// Failed-login lockout: read the stored tuning at startup and re-read it every
	// minute, so an operator can tighten it during a credential-stuffing wave, or
	// loosen it when legitimate users are being locked out, without a restart.
	authlimit.Apply("hermex-webmail2", api.Limiter(), dir.GetLoginLockoutSettings)
	go authlimit.RunMaintenance("hermex-webmail2", api.Limiter(), dir.GetLoginLockoutSettings)
	api.Pub = publicfolder.New(cfg)             // per-domain public folders, rooted at the config's HomedirFor
	api.DigestSecret = []byte(cfg.DigestSecret) // verifies quarantine-digest release links (empty disables them)

	// Webmail request-body cap: read at startup and re-read every minute so an admin's
	// change applies without a restart; 0 keeps the built-in default.
	applyWebmailSizeLimit(dir.GetSizeLimits, webmail2api.SetMaxRequestBody)
	go runWebmailSizeMaintenance(dir.GetSizeLimits, webmail2api.SetMaxRequestBody)
	addr := cfg.Webmail2Addr
	if addr == "" {
		addr = ":8080"
	}
	// Per-client HTTP request limiter: read the stored settings at startup and
	// re-read them every minute, so an operator's change applies without a restart.
	// It is off until an operator enables it, and any read failure leaves it as it
	// is, so a settings problem never starts throttling clients. It caps requests
	// of every kind; the failed-login throttle in internal/authlimit is separate and
	// stays in place.
	// Outbound abuse limiting: this daemon queues external mail through
	// DeliverAndRelay, so a compromised account must meet the same per-account
	// recipient cap SMTP submission enforces. It starts disabled and follows the
	// stored settings without a restart.
	mta.StartOutboundLimiter("hermex-webmail2", logger, dir.GetOutboundSettings)
	httpLimiter := httplimit.NewLimiter()
	httplimit.Apply("hermex-webmail2", httpLimiter, dir.GetHTTPRateLimitSettings)
	go httplimit.RunMaintenance("hermex-webmail2", httpLimiter, dir.GetHTTPRateLimitSettings)
	// TLS certificates come from the provider: the config-file cert as a fallback,
	// overridden by an admin-uploaded cert the provider polls for, so a renewal
	// applies without a restart.
	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		log.Fatalf("hermex-webmail2: tls: %v", err)
	}
	if provider.TLSEnabled() {
		go provider.RunMaintenance()
	}
	hs, err := serve.New(addr, api.Handler(), provider, logger, logging.Webmail, httpLimiter)
	if err != nil {
		log.Fatalf("hermex-webmail2: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "webmail2", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Web push: poll push subscribers' inboxes and notify their devices of new mail.
	api.StartPushPoller(ctx, 15*time.Second)
	log.Printf("hermex-webmail2 listening on %s", addr)
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider.TLSEnabled() {
		// Report the serving certificate's remaining validity, so a renewal that
		// failed shows as degraded before clients start failing handshakes.
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "webmail2", checks...)...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, spool.Close, logClose, db.Close); err != nil {
		log.Fatalf("hermex-webmail2: %v", err)
	}
}

// applyWebmailSizeLimit reads the stored webmail request-body cap and applies it. A
// missing row or a read error leaves the cap unchanged, so a settings failure never
// shrinks it unexpectedly.
func applyWebmailSizeLimit(read func() (directory.SizeLimits, bool, error), setRequestBody func(int64)) {
	s, found, err := read()
	if err != nil {
		log.Printf("hermex-webmail2: size limits read failed, leaving the request cap unchanged: %v", err)
		return
	}
	if !found {
		return
	}
	setRequestBody(s.WebmailRequestBytes)
}

// runWebmailSizeMaintenance re-applies the webmail request-body cap every minute so an
// admin change takes effect without a restart. It runs until the process exits.
func runWebmailSizeMaintenance(read func() (directory.SizeLimits, bool, error), setRequestBody func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyWebmailSizeLimit(read, setRequestBody)
	}
}
