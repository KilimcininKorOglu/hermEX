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

	cfg, db, dir, logger, logClose := openDirectory(*cfgPath)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log
	mta.SetDefaultLogger(logger)         // post-delivery pass failures route to the central log
	webmail2api.SetDefaultLogger(logger) // best-effort API failures (a Sent copy that could not be filed) route there too

	// Antivirus: install the package-level scanner from clamd_addr (a no-op when
	// unset), so authenticated submissions are scanned before relay.
	mta.EnableScanning(cfg.ClamdAddr, dir, cfg.QuarantinePath, cfg.Hostname, logger)

	spool := openSpool(cfg, dir, logger)

	// The session cookie is marked Secure when the front door terminates TLS, which
	// the shared config signals via a configured certificate.
	api := webmail2api.NewServer(dir, dir, spool, cfg.Hostname, []byte(cfg.Webmail2Secret), cfg.Webmail2Dist, cfg.TLSCert != "")
	// Failed-login lockout: read the stored tuning at startup and re-read it every
	// minute, so an operator can tighten it during a credential-stuffing wave, or
	// loosen it when legitimate users are being locked out, without a restart.
	authlimit.Apply("hermex-webmail2", logger, api.Limiter(), dir.GetLoginLockoutSettings)
	go authlimit.RunMaintenance("hermex-webmail2", logger, api.Limiter(), dir.GetLoginLockoutSettings)
	api.Pub = publicfolder.New(cfg)             // per-domain public folders, rooted at the config's HomedirFor
	api.DigestSecret = []byte(cfg.DigestSecret) // verifies quarantine-digest release links (empty disables them)

	// Webmail request-body cap: read at startup and re-read every minute so an admin's
	// change applies without a restart; 0 keeps the built-in default.
	applyWebmailSizeLimit(logger, dir.GetSizeLimits, webmail2api.SetMaxRequestBody, webmail2api.SetMaxFreeBusyTargets, webmail2api.SetMaxPreviewBytes)
	go runWebmailSizeMaintenance(logger, dir.GetSizeLimits, webmail2api.SetMaxRequestBody, webmail2api.SetMaxFreeBusyTargets, webmail2api.SetMaxPreviewBytes)
	addr := orDefault(cfg.Webmail2Addr, ":8080")
	// Outbound abuse limiting: this daemon queues external mail through
	// DeliverAndRelay, so a compromised account must meet the same per-account
	// recipient cap SMTP submission enforces. It starts disabled and follows the
	// stored settings without a restart.
	mta.StartOutboundLimiter("hermex-webmail2", logger, dir.GetOutboundSettings)
	mta.StartAutoReply("hermex-webmail2", logger, dir.GetAutoReplySettings)
	// The operator's inbound message size limit applies to this daemon's sends
	// too: SMTP refuses an oversized message during DATA, and nothing here ever
	// reaches an SMTP session.
	mta.StartMessageSizeLimit("hermex-webmail2", logger, dir.GetMessageSizeSettings)
	// Per-client HTTP request limiter: read the stored settings at startup and
	// re-read them every minute, so an operator's change applies without a restart.
	// It is off until an operator enables it, and any read failure leaves it as it
	// is, so a settings problem never starts throttling clients. It caps requests
	// of every kind; the failed-login throttle in internal/authlimit is separate and
	// stays in place.
	httpLimiter := httplimit.NewLimiter()
	httplimit.Apply("hermex-webmail2", logger, httpLimiter, dir.GetHTTPRateLimitSettings)
	go httplimit.RunMaintenance("hermex-webmail2", logger, httpLimiter, dir.GetHTTPRateLimitSettings)
	provider := startTLS(cfg, dir, logger)
	hs, err := serve.New(addr, api.Handler(), provider, logger, logging.Webmail, httpLimiter)
	if err != nil {
		log.Fatalf("hermex-webmail2: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "webmail2", "addr": addr})
	log.Printf("hermex-webmail2 listening on %s", addr)
	runUntilSignal(cfg, db, dir, logger, provider, api, hs, spool.Close, logClose)
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
// accounts behind it, nor sign a session without its own secret.
func openDirectory(cfgPath string) (*config.Config, *sql.DB, *directory.SQLDirectory, *logging.Logger, func() error) {
	cfg, err := config.Load(cfgPath)
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
	// At-rest wrapping for the private keys the directory stores (DKIM signing
	// keys, uploaded TLS keys). An unset secret leaves them in plaintext and says
	// so on startup.
	dir.SetKeySecret(cfg.KeyWrapSecret())
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-webmail2: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build("hermex-webmail2", cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	return cfg, db, dir, logger, logClose
}

// openSpool opens the shared relay spool the MTA drains. Composed mail with external
// recipients is enqueued there, DKIM-signed with the sending domain's key as it is
// spooled.
func openSpool(cfg *config.Config, dir *directory.SQLDirectory, logger *logging.Logger) *relay.Spool {
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-webmail2: open relay spool: %v", err)
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
		log.Fatalf("hermex-webmail2: tls: %v", err)
	}
	if provider.TLSEnabled() {
		go provider.RunMaintenance()
	}
	return provider
}

// runUntilSignal starts the push poller and the session prune, then serves until a
// shutdown signal arrives, drains the server and runs the cleanups in order.
func runUntilSignal(cfg *config.Config, db *sql.DB, dir *directory.SQLDirectory, logger *logging.Logger, provider *tlscert.Provider, api *webmail2api.Server, hs lifecycle.Component, cleanups ...func() error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Web push: poll push subscribers' inboxes and notify their devices of new mail.
	api.StartPushPoller(ctx, 15*time.Second)
	go runWebmailSessionPrune(ctx, dir, logger)
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider.TLSEnabled() {
		// Report the serving certificate's remaining validity, so a renewal that
		// failed shows as degraded before clients start failing handshakes.
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "webmail2", checks...)...)
	cleanups = append(cleanups, db.Close)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, cleanups...); err != nil {
		log.Fatalf("hermex-webmail2: %v", err)
	}
}

// applyWebmailSizeLimit reads the stored webmail request-body cap and applies it. A
// missing row or a read error leaves the cap unchanged, so a settings failure never
// shrinks it unexpectedly.
func applyWebmailSizeLimit(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setRequestBody, setFreeBusyTargets, setPreviewBytes func(int64)) {
	s, found, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, "hermex-webmail2", "size-limits", "leaving the request cap unchanged", err)
		return
	}
	if !found {
		return
	}
	setRequestBody(s.WebmailRequestBytes)
	setFreeBusyTargets(s.FreeBusyMaxTargets)
	setPreviewBytes(s.WebmailPreviewMaxBytes)
}

// runWebmailSizeMaintenance re-applies the webmail request-body cap every minute so an
// admin change takes effect without a restart. It runs until the process exits.
func runWebmailSizeMaintenance(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setRequestBody, setFreeBusyTargets, setPreviewBytes func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyWebmailSizeLimit(logger, read, setRequestBody, setFreeBusyTargets, setPreviewBytes)
	}
}

// runWebmailSessionPrune deletes expired webmail session rows once a minute. Every
// read already filters on expiry, so an expired row is inert, but nothing removed it:
// each login that ended by the browser closing rather than by an explicit logout left
// a row behind forever. Guarded so several instances do not scan the same table at
// once; a refusal or a lock error simply skips this pass.
func runWebmailSessionPrune(ctx context.Context, dir *directory.SQLDirectory, logger *logging.Logger) {
	sweep := func() {
		release, ok, err := dir.TryLock(ctx, directory.LockWebmailSessionPrune)
		if err != nil {
			logger.Info(logging.Webmail, "session.prune.lock.fail", logging.Fields{"error": err.Error()})
			return
		}
		if !ok {
			return
		}
		defer release()
		if _, err := dir.PurgeExpiredWebmailSessions(time.Now().Unix()); err != nil {
			logger.Info(logging.Webmail, "session.prune.fail", logging.Fields{"error": err.Error()})
		}
	}
	sweep()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
