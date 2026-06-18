// Command dav runs the hermEX CalDAV/CardDAV HTTP server: it authenticates users
// against the directory database with HTTP Basic and serves their contacts (and,
// later, calendars) from the store.
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
	"hermex/internal/dav"
	"hermex/internal/directory"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/serve"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-dav: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-dav: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-dav: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir, cfg.LogRetentionDays)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	srv := dav.NewServer(dir, dir, cfg.Hostname)
	addr := cfg.DAVAddr
	if addr == "" {
		addr = ":8080"
	}
	hs, err := serve.New(addr, srv.Handler(), cfg, logger, logging.DAV)
	if err != nil {
		log.Fatalf("hermex-dav: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "dav", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-dav listening on %s", addr)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, []lifecycle.Component{hs}, logClose, db.Close); err != nil {
		log.Fatalf("hermex-dav: %v", err)
	}
}
