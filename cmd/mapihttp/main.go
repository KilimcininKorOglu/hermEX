// Command mapihttp runs the hermEX native Outlook transport server (MAPI/HTTP,
// [MS-OXCMAPIHTTP]): it authenticates users against the directory database with
// HTTP Basic and serves the EMSMDB (/mapi/emsmdb) and NSPI (/mapi/nspi)
// endpoints.
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
	"hermex/internal/health"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mapihttp"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
	"hermex/internal/serve"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-mapi: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-mapi: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-mapi: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	// Enqueue external recipients of submitted mail into the shared relay spool the
	// MTA drains; without it native Outlook would send local-only.
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-mapi: open relay spool: %v", err)
	}
	srv := mapihttp.NewServer(dir, dir, cfg.Hostname, spool)
	srv.Logger = logger
	addr := cfg.MapiAddr
	if addr == "" {
		addr = ":8080"
	}
	hs, err := serve.New(addr, srv.Handler(), cfg, logger, logging.MAPI)
	if err != nil {
		log.Fatalf("hermex-mapi: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "mapihttp", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-mapi listening on %s", addr)
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "mapi", health.Check{Name: "directory", Probe: db.PingContext})...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, spool.Close, logClose, db.Close); err != nil {
		log.Fatalf("hermex-mapi: %v", err)
	}
}
