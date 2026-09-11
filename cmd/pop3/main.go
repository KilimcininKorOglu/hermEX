// Command pop3 runs the hermEX POP3 retrieval daemon: it authenticates users
// against the directory database and serves their mailboxes.
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
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/pop3"
	"hermex/internal/serve"
	"hermex/internal/tlscert"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, db, dir, logger, logClose := openDirectory(*cfgPath)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	addr, ln := listenPOP3(cfg)
	srv := &pop3.Server{Auth: dir, Hostname: cfg.Hostname, Logger: logger, Limiter: authlimit.New(0, 0, 0)}
	// Failed-login lockout: read the stored tuning at startup and re-read it every
	// minute, so an operator can tighten it during a credential-stuffing wave, or
	// loosen it when legitimate users are being locked out, without a restart.
	authlimit.Apply("hermex-pop3", logger, srv.Limiter, dir.GetLoginLockoutSettings)
	go authlimit.RunMaintenance("hermex-pop3", logger, srv.Limiter, dir.GetLoginLockoutSettings)
	// Concurrent-connection cap: read the stored tuning at startup and re-read it
	// every minute, so an operator can bound the daemon during a connection flood
	// without a restart. It starts disabled until an operator turns it on.
	conns := connlimit.New()
	connlimit.Apply("hermex-pop3", logger, conns, dir.GetConnLimitSettings)
	go connlimit.RunMaintenance("hermex-pop3", logger, conns, dir.GetConnLimitSettings)
	srv.SetConnLimiter(conns)
	// POP3 command-line cap: read at startup and re-read every minute so an admin's
	// change applies without a restart; 0 keeps the built-in default.
	applyPOP3SizeLimit(logger, dir.GetSizeLimits, srv.SetMaxCommandLine)
	go runPOP3SizeMaintenance(logger, dir.GetSizeLimits, srv.SetMaxCommandLine)
	provider := startTLS(cfg, dir, logger, srv)
	srv.AddListener(ln)
	log.Printf("hermex-pop3 listening on %s", addr)
	addImplicitTLS(cfg, provider, srv)

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "pop3", "addr": addr})
	runUntilSignal(cfg, db, provider, srv, logClose)
}

// openDirectory loads the config, opens the directory database and builds the
// logger. Every failure here is fatal: the daemon cannot serve a mailbox without
// the accounts behind it.
func openDirectory(cfgPath string) (*config.Config, *sql.DB, *directory.SQLDirectory, *logging.Logger, func() error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("hermex-pop3: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-pop3: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-pop3: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	// At-rest wrapping for the private keys the directory stores (DKIM signing
	// keys, uploaded TLS keys). An unset secret leaves them in plaintext and says
	// so on startup.
	dir.SetKeySecret(cfg.KeyWrapSecret())
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-pop3: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build("hermex-pop3", cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	return cfg, db, dir, logger, logClose
}

// listenPOP3 opens the plaintext listener on the configured address.
func listenPOP3(cfg *config.Config) (string, net.Listener) {
	addr := cfg.POP3Addr
	if addr == "" {
		addr = ":110"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("hermex-pop3: listen %s: %v", addr, err)
	}
	return addr, ln
}

// startTLS resolves the serving certificate: the config-file cert as a fallback,
// overridden by an admin-uploaded cert the provider polls for, so a renewal applies
// without a restart.
func startTLS(cfg *config.Config, dir *directory.SQLDirectory, logger *logging.Logger, srv *pop3.Server) *tlscert.Provider {
	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		log.Fatalf("hermex-pop3: tls: %v", err)
	}
	if provider.TLSEnabled() {
		tc, _ := provider.TLSConfig()
		srv.TLSConfig = tc // enables STLS on the plaintext listener
		go provider.RunMaintenance()
	}
	return provider
}

// addImplicitTLS serves the optional implicit-TLS listener (e.g. :995) alongside the
// plaintext one; the stateless server handles both concurrently.
func addImplicitTLS(cfg *config.Config, provider *tlscert.Provider, srv *pop3.Server) {
	if !provider.TLSEnabled() || cfg.POP3SAddr == "" {
		return
	}
	tln, err := serve.TLSListener(cfg.POP3SAddr, provider)
	if err != nil {
		log.Fatalf("hermex-pop3: implicit TLS on %s: %v", cfg.POP3SAddr, err)
	}
	srv.AddListener(tln)
	log.Printf("hermex-pop3 listening on %s (implicit TLS)", cfg.POP3SAddr)
}

// applyPOP3SizeLimit reads the stored POP3 command-line cap and applies it to the
// server. A missing row or a read error leaves the cap unchanged, so a settings
// failure never shrinks the limit unexpectedly.
func applyPOP3SizeLimit(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setLine func(int64)) {
	s, found, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, "hermex-pop3", "size-limits", "leaving the command-line cap unchanged", err)
		return
	}
	if !found {
		return
	}
	setLine(s.POP3CommandLineBytes)
}

// runPOP3SizeMaintenance re-applies the cap every minute so an admin change takes
// effect without a restart. It runs until the process exits.
func runPOP3SizeMaintenance(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setLine func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyPOP3SizeLimit(logger, read, setLine)
	}
}

// runUntilSignal serves until a shutdown signal arrives, then drains the server and
// closes the log sink and the directory database.
func runUntilSignal(cfg *config.Config, db *sql.DB, provider *tlscert.Provider, srv *pop3.Server, logClose func() error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider.TLSEnabled() {
		// Report the serving certificate's remaining validity, so a renewal that
		// failed shows as degraded before clients start failing handshakes.
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	comps := append([]lifecycle.Component{srv},
		health.Components(cfg.HealthAddr, "pop3", checks...)...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, logClose, db.Close); err != nil {
		log.Fatalf("hermex-pop3: %v", err)
	}
}
