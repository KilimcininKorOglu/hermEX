// Command notify runs the hermEX central push-notification relay: a tiny HTTP
// server that fans mailbox-change wake signals from the writer daemons out to the
// long-poll consumers over Server-Sent Events. It holds no database and no mailbox
// store — it is a pure in-memory relay — so it starts instantly and depends on
// nothing. The mail path never hard-depends on it: when it is down, producers fail
// their publish silently and consumers fall back to polling.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"hermex/internal/config"
	"hermex/internal/health"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/notifyd"
	"hermex/internal/serve"
)

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-notify: %v", err)
	}
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)

	srv := notifyd.New(cfg.NotifySecret, logger)
	addr := cfg.NotifyAddr
	if addr == "" {
		addr = ":8080"
	}
	hs, err := serve.New(addr, srv.Handler(), cfg, logger, logging.Notify)
	if err != nil {
		log.Fatalf("hermex-notify: %v", err)
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "notify", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hermex-notify listening on %s", addr)
	comps := append([]lifecycle.Component{hs}, health.Components(cfg.HealthAddr, "notify")...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, logClose); err != nil {
		log.Fatalf("hermex-notify: %v", err)
	}
}
