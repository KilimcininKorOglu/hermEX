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

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/pop3"
	"hermex/internal/serve"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
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
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir, cfg.LogRetentionDays)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	addr := cfg.POP3Addr
	if addr == "" {
		addr = ":110"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("hermex-pop3: listen %s: %v", addr, err)
	}
	srv := &pop3.Server{Auth: dir, Hostname: cfg.Hostname, Logger: logger}
	if cfg.TLSEnabled() {
		tc, err := cfg.TLSConfig()
		if err != nil {
			log.Fatalf("hermex-pop3: tls: %v", err)
		}
		srv.TLSConfig = tc // enables STLS on the plaintext listener
	}
	srv.AddListener(ln)
	log.Printf("hermex-pop3 listening on %s", addr)

	// Optional implicit-TLS listener (e.g. :995) served alongside the plaintext
	// one; the stateless server handles both concurrently.
	if cfg.TLSEnabled() && cfg.POP3SAddr != "" {
		tln, err := serve.TLSListener(cfg.POP3SAddr, cfg)
		if err != nil {
			log.Fatalf("hermex-pop3: implicit TLS on %s: %v", cfg.POP3SAddr, err)
		}
		srv.AddListener(tln)
		log.Printf("hermex-pop3 listening on %s (implicit TLS)", cfg.POP3SAddr)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "pop3", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, []lifecycle.Component{srv}, logClose, db.Close); err != nil {
		log.Fatalf("hermex-pop3: %v", err)
	}
}
