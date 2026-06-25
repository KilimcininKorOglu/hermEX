// Command webmail2 runs the hermEX webmail2 server: it serves the single-page
// app and its /api/v1 JSON API, authenticating users against the directory
// database and serving their mailboxes from the object store.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/dkimsign"
	"hermex/internal/health"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/publicfolder"
	"hermex/internal/relay"
	"hermex/internal/serve"
	"hermex/internal/webmail2api"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-webmail2: %v", err)
	}
	if cfg.Webmail2Secret == "" {
		log.Fatalf("hermex-webmail2: webmail2_secret is required")
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-webmail2: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-webmail2: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-webmail2: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	// Composed mail with external recipients is enqueued into the shared relay spool
	// the MTA drains, DKIM-signed with the sending domain's key as it is spooled.
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-webmail2: open relay spool: %v", err)
	}
	spool.Signer = &dkimsign.Signer{Keys: dir, Logger: logger}

	// The session cookie is marked Secure when the front door terminates TLS, which
	// the shared config signals via a configured certificate.
	api := webmail2api.NewServer(dir, dir, spool, cfg.Hostname, []byte(cfg.Webmail2Secret), cfg.Webmail2Dist, cfg.TLSCert != "")
	api.Pub = publicfolder.New(cfg)             // per-domain public folders, rooted at the config's HomedirFor
	api.DigestSecret = []byte(cfg.DigestSecret) // verifies quarantine-digest release links (empty disables them)

	addr := cfg.Webmail2Addr
	if addr == "" {
		addr = ":8080"
	}
	hs, err := serve.New(addr, api.Handler(), cfg, logger, logging.Webmail)
	if err != nil {
		log.Fatalf("hermex-webmail2: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "webmail2", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-webmail2 listening on %s", addr)
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "webmail2", health.Check{Name: "directory", Probe: db.PingContext})...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, spool.Close, logClose, db.Close); err != nil {
		log.Fatalf("hermex-webmail2: %v", err)
	}
}
