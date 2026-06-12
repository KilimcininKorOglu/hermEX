// Command imap runs the hermEX IMAP retrieval daemon: it authenticates users
// against the directory database and serves their mailboxes over RFC 3501.
package main

import (
	"database/sql"
	"flag"
	"log"
	"net"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/imap"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-imap: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-imap: open directory: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-imap: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)

	addr := cfg.IMAPAddr
	if addr == "" {
		addr = ":143"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("hermex-imap: listen %s: %v", addr, err)
	}
	srv := &imap.Server{Auth: dir, Hostname: cfg.Hostname}
	log.Printf("hermex-imap listening on %s", addr)
	log.Fatalf("hermex-imap: %v", srv.Serve(ln))
}
