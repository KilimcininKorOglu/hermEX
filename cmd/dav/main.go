// Command dav runs the hermEX CalDAV/CardDAV HTTP server: it authenticates users
// against the directory database with HTTP Basic and serves their contacts (and,
// later, calendars) from the store.
package main

import (
	"database/sql"
	"flag"
	"log"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/dav"
	"hermex/internal/directory"
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
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-dav: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)

	srv := dav.NewServer(dir, dir, cfg.Hostname)
	addr := cfg.DAVAddr
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("hermex-dav listening on %s", addr)
	log.Fatalf("hermex-dav: %v", serve.ListenAndServe(addr, srv.Handler(), cfg))
}
