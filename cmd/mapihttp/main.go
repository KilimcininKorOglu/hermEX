// Command mapihttp runs the hermEX native Outlook transport server (MAPI/HTTP,
// [MS-OXCMAPIHTTP]): it authenticates users against the directory database with
// HTTP Basic and serves the EMSMDB (/mapi/emsmdb) and NSPI (/mapi/nspi)
// endpoints.
package main

import (
	"database/sql"
	"flag"
	"log"
	"net/http"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/mapihttp"
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
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-mapi: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)

	srv := mapihttp.NewServer(dir, dir, cfg.Hostname)
	addr := cfg.MapiAddr
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("hermex-mapi listening on %s", addr)
	log.Fatalf("hermex-mapi: %v", http.ListenAndServe(addr, srv.Handler()))
}
