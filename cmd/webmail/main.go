// Command webmail runs the hermEX webmail HTTP server: it authenticates users
// against the directory database and serves their mailboxes from the store.
package main

import (
	"database/sql"
	"flag"
	"log"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
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
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-webmail: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)

	srv, err := webmail.NewServer(dir, dir, cfg.Hostname)
	if err != nil {
		log.Fatalf("hermex-webmail: %v", err)
	}
	addr := cfg.WebmailAddr
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("hermex-webmail listening on %s", addr)
	log.Fatalf("hermex-webmail: %v", serve.ListenAndServe(addr, srv.Handler(), cfg))
}
