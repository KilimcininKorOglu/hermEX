// Command ews runs the hermEX Exchange Web Services (EWS) HTTP server: it
// authenticates users against the directory database with HTTP Basic and serves
// their mailbox over SOAP on /EWS/Exchange.asmx.
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
	"hermex/internal/ews"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/serve"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-ews: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-ews: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-ews: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)

	srv := ews.NewServer(dir, dir, cfg.Hostname)
	addr := cfg.EWSAddr
	if addr == "" {
		addr = ":8080"
	}
	hs, err := serve.New(addr, srv.Handler(), cfg)
	if err != nil {
		log.Fatalf("hermex-ews: %v", err)
	}

	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir, cfg.LogRetentionDays)
	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "ews", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-ews listening on %s", addr)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, []lifecycle.Component{hs}, logClose, db.Close); err != nil {
		log.Fatalf("hermex-ews: %v", err)
	}
}
