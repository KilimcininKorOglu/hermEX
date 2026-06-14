// Command activesync runs the hermEX Exchange ActiveSync (EAS) HTTP server: it
// authenticates users against the directory database with HTTP Basic and serves
// the MS-ASHTTP endpoint plus mobilesync Autodiscover, syncing their mailbox to
// phones over MS-ASCMD/MS-ASWBXML.
package main

import (
	"database/sql"
	"flag"
	"log"
	"net/http"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/activesync"
	"hermex/internal/config"
	"hermex/internal/directory"
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
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-activesync: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)

	srv := activesync.NewServer(dir, dir, cfg.Hostname)
	addr := cfg.ActiveSyncAddr
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("hermex-activesync listening on %s", addr)
	log.Fatalf("hermex-activesync: %v", http.ListenAndServe(addr, srv.Handler()))
}
