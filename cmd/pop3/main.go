// Command pop3 runs the hermEX POP3 retrieval daemon: it authenticates users
// against the directory database and serves their mailboxes.
package main

import (
	"database/sql"
	"flag"
	"log"
	"net"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/pop3"
	"hermex/internal/serve"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-pop3: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-pop3: open directory: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-pop3: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)

	addr := cfg.POP3Addr
	if addr == "" {
		addr = ":110"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("hermex-pop3: listen %s: %v", addr, err)
	}
	srv := &pop3.Server{Auth: dir, Hostname: cfg.Hostname}

	// Optional implicit-TLS listener (e.g. :995) served alongside the plaintext
	// one; the stateless server handles both concurrently.
	if cfg.TLSEnabled() && cfg.POP3SAddr != "" {
		tln, err := serve.TLSListener(cfg.POP3SAddr, cfg)
		if err != nil {
			log.Fatalf("hermex-pop3: implicit TLS on %s: %v", cfg.POP3SAddr, err)
		}
		log.Printf("hermex-pop3 listening on %s (implicit TLS)", cfg.POP3SAddr)
		go func() { log.Fatalf("hermex-pop3: %v", srv.Serve(tln)) }()
	}

	log.Printf("hermex-pop3 listening on %s", addr)
	log.Fatalf("hermex-pop3: %v", srv.Serve(ln))
}
