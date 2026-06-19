// Command activesync runs the hermEX Exchange ActiveSync (EAS) HTTP server: it
// authenticates users against the directory database with HTTP Basic and serves
// the MS-ASHTTP endpoint plus mobilesync Autodiscover, syncing their mailbox to
// phones over MS-ASCMD/MS-ASWBXML.
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

	"hermex/internal/activesync"
	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
	"hermex/internal/serve"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-activesync: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-activesync: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-activesync: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir, cfg.LogRetentionDays)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	srv := activesync.NewServer(dir, dir, cfg.Hostname)
	srv.Logger = logger
	// Enqueue external recipients of SendMail into the shared relay spool the MTA
	// drains; without it ActiveSync would send local-only.
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-activesync: open relay spool: %v", err)
	}
	srv.Spool = spool
	addr := cfg.ActiveSyncAddr
	if addr == "" {
		addr = ":8080"
	}
	hs, err := serve.New(addr, srv.Handler(), cfg, logger, logging.ActiveSync)
	if err != nil {
		log.Fatalf("hermex-activesync: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "activesync", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-activesync listening on %s", addr)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, []lifecycle.Component{hs}, spool.Close, logClose, db.Close); err != nil {
		log.Fatalf("hermex-activesync: %v", err)
	}
}
