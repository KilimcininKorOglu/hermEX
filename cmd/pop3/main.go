// Command pop3 runs the hermEX POP3 retrieval daemon: it authenticates users
// against the configured accounts and serves their mailboxes.
package main

import (
	"flag"
	"log"
	"net"

	"hermex/internal/config"
	"hermex/internal/pop3"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-pop3: %v", err)
	}
	addr := cfg.POP3Addr
	if addr == "" {
		addr = ":110"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("hermex-pop3: listen %s: %v", addr, err)
	}
	srv := &pop3.Server{Auth: cfg.StaticAccounts(), Hostname: cfg.Hostname}
	log.Printf("hermex-pop3 listening on %s", addr)
	log.Fatalf("hermex-pop3: %v", srv.Serve(ln))
}
