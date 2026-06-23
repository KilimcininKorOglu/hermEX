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

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/health"
	"hermex/internal/imap"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/publicfolder"
	"hermex/internal/serve"
	"hermex/internal/tlscert"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
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
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	addr := cfg.IMAPAddr
	if addr == "" {
		addr = ":143"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("hermex-imap: listen %s: %v", addr, err)
	}
	srv := &imap.Server{Auth: dir, Hostname: cfg.Hostname, Logger: logger, Pub: publicfolder.New(cfg)}
	// IMAP literal size cap: read at startup and re-read every minute so an admin's
	// change applies without a restart; 0 keeps the built-in default.
	applyIMAPSizeLimit(dir.GetSizeLimits, srv.SetMaxLiteralSize)
	go runIMAPSizeMaintenance(dir.GetSizeLimits, srv.SetMaxLiteralSize)
	// TLS certificates come from the provider: the config-file cert as a fallback,
	// overridden by an admin-uploaded cert the provider polls for, so a renewal
	// applies without a restart.
	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		log.Fatalf("hermex-imap: tls: %v", err)
	}
	if provider.TLSEnabled() {
		tc, _ := provider.TLSConfig()
		srv.TLSConfig = tc // enables STARTTLS on the plaintext listener
		go provider.RunMaintenance()
	}
	srv.AddListener(ln)
	log.Printf("hermex-imap listening on %s", addr)

	// Optional implicit-TLS listener (e.g. :993) served alongside the plaintext
	// one; the stateless server handles both concurrently.
	if provider.TLSEnabled() && cfg.IMAPSAddr != "" {
		tln, err := serve.TLSListener(cfg.IMAPSAddr, provider)
		if err != nil {
			log.Fatalf("hermex-imap: implicit TLS on %s: %v", cfg.IMAPSAddr, err)
		}
		srv.AddListener(tln)
		log.Printf("hermex-imap listening on %s (implicit TLS)", cfg.IMAPSAddr)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "imap", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	comps := append([]lifecycle.Component{srv},
		health.Components(cfg.HealthAddr, "imap", health.Check{Name: "directory", Probe: db.PingContext})...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, logClose, db.Close); err != nil {
		log.Fatalf("hermex-imap: %v", err)
	}
}

// applyIMAPSizeLimit reads the stored IMAP literal cap and applies it to the server. A
// missing row or a read error leaves the cap unchanged, so a settings failure never
// shrinks the limit unexpectedly.
func applyIMAPSizeLimit(read func() (directory.SizeLimits, bool, error), setLiteral func(int64)) {
	s, found, err := read()
	if err != nil {
		log.Printf("hermex-imap: size limits read failed, leaving the literal cap unchanged: %v", err)
		return
	}
	if !found {
		return
	}
	setLiteral(s.IMAPLiteralBytes)
}

// runIMAPSizeMaintenance re-applies the IMAP literal cap every minute so an admin
// change takes effect without a restart. It runs until the process exits.
func runIMAPSizeMaintenance(read func() (directory.SizeLimits, bool, error), setLiteral func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyIMAPSizeLimit(read, setLiteral)
	}
}
