// Command imap runs the hermEX IMAP retrieval daemon: it authenticates users
// against the directory database and serves their mailboxes over RFC 3501.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/authlimit"
	"hermex/internal/config"
	"hermex/internal/connlimit"
	"hermex/internal/directory"
	"hermex/internal/health"
	"hermex/internal/imap"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mta"
	"hermex/internal/notify"
	"hermex/internal/objectstore"
	"hermex/internal/publicfolder"
	"hermex/internal/serve"
	"hermex/internal/tlscert"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, db, dir, logger, logClose := openDirectory(*cfgPath)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	addr, ln := listenIMAP(cfg)
	// Push notifications: publish this daemon's own mailbox writes, and subscribe so
	// an IDLE-ing client wakes the instant its mailbox changes instead of on the IDLE
	// poll cadence. No-ops when notify_url is empty.
	notify.EnableProducer(cfg.NotifyURL, cfg.NotifySecret, logger)

	// Antivirus: install the package-level scanner from clamd_addr (a no-op when
	// unset), so an APPEND cannot park malware in a mailbox unscanned. Every other
	// daemon that stores client-supplied content already does this.
	mta.EnableScanning(cfg.ClamdAddr, dir, cfg.QuarantinePath, cfg.Hostname, logger)
	srv := &imap.Server{Auth: dir, Accounts: dir, Hostname: cfg.Hostname, Logger: logger, Pub: publicfolder.New(cfg), Limiter: authlimit.New(0, 0, 0)}
	// Failed-login lockout: read the stored tuning at startup and re-read it every
	// minute, so an operator can tighten it during a credential-stuffing wave, or
	// loosen it when legitimate users are being locked out, without a restart.
	authlimit.Apply("hermex-imap", logger, srv.Limiter, dir.GetLoginLockoutSettings)
	go authlimit.RunMaintenance("hermex-imap", logger, srv.Limiter, dir.GetLoginLockoutSettings)
	// Concurrent-connection cap: read the stored tuning at startup and re-read it
	// every minute, so an operator can bound the daemon during a connection flood
	// without a restart. It starts disabled until an operator turns it on.
	conns := connlimit.New()
	connlimit.Apply("hermex-imap", logger, conns, dir.GetConnLimitSettings)
	go connlimit.RunMaintenance("hermex-imap", logger, conns, dir.GetConnLimitSettings)
	srv.SetConnLimiter(conns)
	srv.SetNotify(notify.EnableConsumer(cfg.NotifyURL, cfg.NotifySecret, logger))
	// IMAP literal size cap: read at startup and re-read every minute so an admin's
	// change applies without a restart; 0 keeps the built-in default.
	applyIMAPSizeLimit(logger, dir.GetSizeLimits, srv.SetMaxLiteralSize)
	go runIMAPSizeMaintenance(logger, dir.GetSizeLimits, srv.SetMaxLiteralSize)
	provider := startTLS(cfg, dir, logger, srv)
	srv.AddListener(ln)
	log.Printf("hermex-imap listening on %s", addr)
	addImplicitTLS(cfg, provider, srv)

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "imap", "addr": addr})
	runUntilSignal(cfg, db, provider, srv, logClose)
}

// openDirectory loads the config, opens the directory database and builds the
// logger. Every failure here is fatal: the daemon cannot serve a mailbox without
// the accounts behind it.
func openDirectory(cfgPath string) (*config.Config, *sql.DB, *directory.SQLDirectory, *logging.Logger, func() error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("hermex-imap: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-imap: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-imap: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	// At-rest wrapping for the private keys the directory stores (DKIM signing
	// keys, uploaded TLS keys). An unset secret leaves them in plaintext and says
	// so on startup.
	dir.SetKeySecret(cfg.KeyWrapSecret())
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-imap: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build("hermex-imap", cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	return cfg, db, dir, logger, logClose
}

// listenIMAP opens the plaintext listener on the configured address.
func listenIMAP(cfg *config.Config) (string, net.Listener) {
	addr := cfg.IMAPAddr
	if addr == "" {
		addr = ":143"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("hermex-imap: listen %s: %v", addr, err)
	}
	return addr, ln
}

// startTLS resolves the serving certificate: the config-file cert as a fallback,
// overridden by an admin-uploaded cert the provider polls for, so a renewal applies
// without a restart.
func startTLS(cfg *config.Config, dir *directory.SQLDirectory, logger *logging.Logger, srv *imap.Server) *tlscert.Provider {
	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		log.Fatalf("hermex-imap: tls: %v", err)
	}
	if provider.TLSEnabled() {
		tc, _ := provider.TLSConfig()
		srv.TLSConfig = tc // enables STARTTLS on the plaintext listener
		go provider.RunMaintenance()
	}
	return provider
}

// addImplicitTLS serves the optional implicit-TLS listener (e.g. :993) alongside the
// plaintext one; the stateless server handles both concurrently.
func addImplicitTLS(cfg *config.Config, provider *tlscert.Provider, srv *imap.Server) {
	if !provider.TLSEnabled() || cfg.IMAPSAddr == "" {
		return
	}
	tln, err := serve.TLSListener(cfg.IMAPSAddr, provider)
	if err != nil {
		log.Fatalf("hermex-imap: implicit TLS on %s: %v", cfg.IMAPSAddr, err)
	}
	srv.AddListener(tln)
	log.Printf("hermex-imap listening on %s (implicit TLS)", cfg.IMAPSAddr)
}

// runUntilSignal serves until a shutdown signal arrives, then drains the server and
// closes the log sink and the directory database.
func runUntilSignal(cfg *config.Config, db *sql.DB, provider *tlscert.Provider, srv *imap.Server, logClose func() error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider.TLSEnabled() {
		// Report the serving certificate's remaining validity, so a renewal that
		// failed shows as degraded before clients start failing handshakes.
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	comps := append([]lifecycle.Component{srv},
		health.Components(cfg.HealthAddr, "imap", checks...)...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, logClose, db.Close); err != nil {
		log.Fatalf("hermex-imap: %v", err)
	}
}

// applyIMAPSizeLimit reads the stored IMAP literal cap and applies it to the server. A
// missing row or a read error leaves the cap unchanged, so a settings failure never
// shrinks the limit unexpectedly.
func applyIMAPSizeLimit(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setLiteral func(int64)) {
	s, found, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, "hermex-imap", "size-limits", "leaving the literal cap unchanged", err)
		return
	}
	if !found {
		return
	}
	setLiteral(s.IMAPLiteralBytes)
}

// runIMAPSizeMaintenance re-applies the IMAP literal cap every minute so an admin
// change takes effect without a restart. It runs until the process exits.
func runIMAPSizeMaintenance(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setLiteral func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyIMAPSizeLimit(logger, read, setLiteral)
	}
}
