// Command mta runs the hermEX SMTP intake daemon: it accepts mail and delivers
// it into recipient mailboxes resolved through the directory database.
package main

import (
	"database/sql"
	"flag"
	"log"
	"net"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/mta"
	"hermex/internal/smtp"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-mta: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-mta: open directory: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-mta: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)

	addr := cfg.SMTPAddr
	if addr == "" {
		addr = ":25"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("hermex-mta: listen %s: %v", addr, err)
	}
	srv := &smtp.Server{Backend: &mta.Backend{Accounts: dir}, Hostname: cfg.Hostname}
	log.Printf("hermex-mta listening on %s", addr)
	log.Fatalf("hermex-mta: %v", srv.Serve(ln))
}
