// Command dav runs the hermEX CalDAV/CardDAV HTTP server: it authenticates users
// against the directory database with HTTP Basic and serves their contacts (and,
// later, calendars) from the store.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/dav"
	"hermex/internal/directory"
	"hermex/internal/health"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
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
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-dav: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir, cfg.LogRetentionDays)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	srv := dav.NewServer(dir, dir, cfg.Hostname)
	// CalDAV/CardDAV PUT body caps: read at startup and re-read every minute so an
	// admin's change applies without a restart; 0 keeps the built-in defaults.
	applyDAVSizeLimits(dir, srv)
	go runDAVSizeMaintenance(dir, srv)
	addr := cfg.DAVAddr
	if addr == "" {
		addr = ":8080"
	}
	hs, err := serve.New(addr, srv.Handler(), cfg, logger, logging.DAV)
	if err != nil {
		log.Fatalf("hermex-dav: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "dav", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-dav listening on %s", addr)
	comps := append([]lifecycle.Component{hs},
		health.Components(cfg.HealthAddr, "dav", health.Check{Name: "directory", Probe: db.PingContext})...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, logClose, db.Close); err != nil {
		log.Fatalf("hermex-dav: %v", err)
	}
}

// applyDAVSizeLimits reads the stored CalDAV/CardDAV PUT body caps and applies them to
// the server. A missing row or a read error leaves the caps unchanged, so a settings
// failure never shrinks them unexpectedly.
func applyDAVSizeLimits(dir *directory.SQLDirectory, srv *dav.Server) {
	s, found, err := dir.GetSizeLimits()
	if err != nil {
		log.Printf("hermex-dav: size limits read failed, leaving the body caps unchanged: %v", err)
		return
	}
	if !found {
		return
	}
	srv.SetMaxICal(s.DAVICalBytes)
	srv.SetMaxVCard(s.DAVVCardBytes)
}

// runDAVSizeMaintenance re-applies the DAV PUT body caps every minute so an admin
// change takes effect without a restart. It runs until the process exits.
func runDAVSizeMaintenance(dir *directory.SQLDirectory, srv *dav.Server) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyDAVSizeLimits(dir, srv)
	}
}
