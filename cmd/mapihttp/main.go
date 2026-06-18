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
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mapihttp"
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

	srv := mapihttp.NewServer(dir, dir, cfg.Hostname)
	addr := cfg.MapiAddr
	if addr == "" {
		addr = ":8080"
	}
	hs, err := serve.New(addr, srv.Handler(), cfg)
	if err != nil {
		log.Fatalf("hermex-mapi: %v", err)
	}

	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir, cfg.LogRetentionDays)
	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "mapihttp", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-mapi listening on %s", addr)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, []lifecycle.Component{hs}, logClose, db.Close); err != nil {
		log.Fatalf("hermex-mapi: %v", err)
	}
}
