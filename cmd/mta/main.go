// Command mta runs the hermEX SMTP intake daemon: it accepts mail and delivers
// it into recipient mailboxes resolved from the configured accounts.
package main

import (
	"flag"
	"log"
	"net"

	"hermex/internal/config"
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
	addr := cfg.SMTPAddr
	if addr == "" {
		addr = ":25"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("hermex-mta: listen %s: %v", addr, err)
	}
	srv := &smtp.Server{
		Backend:  &mta.Backend{Accounts: cfg.StaticAccounts()},
		Hostname: cfg.Hostname,
	}
	log.Printf("hermex-mta listening on %s", addr)
	log.Fatalf("hermex-mta: %v", srv.Serve(ln))
}
