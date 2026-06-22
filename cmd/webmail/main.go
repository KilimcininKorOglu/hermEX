// Command webmail runs the hermEX webmail HTTP server: it authenticates users
// against the directory database and serves their mailboxes from the store.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/dkimsign"
	"hermex/internal/health"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/publicfolder"
	"hermex/internal/relay"
	"hermex/internal/serve"
	"hermex/internal/webmail"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-webmail: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-webmail: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-webmail: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir, cfg.LogRetentionDays)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	srv, err := webmail.NewServer(dir, dir, cfg.Hostname)
	if err != nil {
		log.Fatalf("hermex-webmail: %v", err)
	}
	srv.Logger = logger
	srv.Pub = publicfolder.New(cfg)             // per-domain public folders rooted at HomedirFor
	srv.DigestSecret = []byte(cfg.DigestSecret) // verifies quarantine-digest release links (empty disables them)
	// Enqueue external recipients of composed mail into the shared relay spool the
	// MTA drains; without it webmail would deliver local-only.
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-webmail: open relay spool: %v", err)
	}
	// DKIM-sign composed mail with the sending domain's enabled key as it is spooled —
	// webmail opens its own spool, so it needs the signer as much as the MTA does.
	spool.Signer = &dkimsign.Signer{Keys: dir, Logger: logger}
	srv.Spool = spool
	addr := cfg.WebmailAddr
	if addr == "" {
		addr = ":8080"
	}
	hs, err := serve.New(addr, srv.Handler(), cfg, logger, logging.Webmail)
	if err != nil {
		log.Fatalf("hermex-webmail: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "webmail", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-webmail listening on %s", addr)
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "webmail", health.Check{Name: "directory", Probe: db.PingContext})...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, spool.Close, logClose, db.Close); err != nil {
		log.Fatalf("hermex-webmail: %v", err)
	}
}
