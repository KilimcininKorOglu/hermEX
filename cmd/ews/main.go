// Command ews runs the hermEX Exchange Web Services (EWS) HTTP server: it
// authenticates users against the directory database with HTTP Basic and serves
// their mailbox over SOAP on /EWS/Exchange.asmx.
package main

import (
	"database/sql"
	"flag"
	"log"
	"net/http"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/ews"
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
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-ews: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)

	srv := ews.NewServer(dir, dir, cfg.Hostname)
	addr := cfg.EWSAddr
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("hermex-ews listening on %s", addr)
	log.Fatalf("hermex-ews: %v", http.ListenAndServe(addr, srv.Handler()))
}
