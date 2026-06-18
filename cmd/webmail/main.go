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
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
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
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir, cfg.LogRetentionDays)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	srv, err := webmail.NewServer(dir, dir, cfg.Hostname)
	if err != nil {
		log.Fatalf("hermex-webmail: %v", err)
	}
	srv.Logger = logger
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
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, []lifecycle.Component{hs}, logClose, db.Close); err != nil {
		log.Fatalf("hermex-webmail: %v", err)
	}
}
