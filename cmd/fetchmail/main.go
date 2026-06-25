// Command fetchmail runs the hermEX fetch-worker daemon: it periodically polls every
// configured remote POP3/IMAP account and delivers new mail into the local mailbox
// through the normal local delivery path.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/fetchmail"
	"hermex/internal/health"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// fetchInterval is how often the worker polls every active source account. A new remote
// message is delivered locally at most this long after it arrives at the source.
const fetchInterval = 5 * time.Minute

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-fetchmail: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-fetchmail: open directory: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-fetchmail: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-fetchmail: schema: %v", err)
	}
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	objectstore.SetDefaultLogger(logger)

	// Deliver a fetched message into the local mailbox through the normal local path.
	// The envelope sender is the null return-path so a fetched message never triggers a
	// local out-of-office reply: the source server already handled the original arrival,
	// and a bulk (fetch-all) pull must not flood the original senders.
	deliver := func(mailbox string, raw []byte, when time.Time) error {
		unresolved, err := mta.Deliver(dir, "", []string{mailbox}, raw, when)
		if err != nil {
			return err
		}
		if len(unresolved) > 0 {
			return fmt.Errorf("local recipient %q does not resolve", mailbox)
		}
		return nil
	}

	wCtx, wCancel := context.WithCancel(context.Background())
	worker := lifecycle.Func{
		StartFn:    func() error { runFetch(wCtx, dir, deliver, fetchInterval, logger); return nil },
		ShutdownFn: func(context.Context) error { wCancel(); return nil },
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "fetchmail"})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	comps := append([]lifecycle.Component{worker},
		health.Components(cfg.HealthAddr, "fetchmail", health.Check{Name: "directory", Probe: db.PingContext})...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, logClose, db.Close); err != nil {
		log.Fatalf("hermex-fetchmail: %v", err)
	}
}

// runFetch polls all active accounts once at startup and then every interval until ctx is
// cancelled. Exactly one process should run this loop: two concurrent pollers could fetch
// and deliver the same message twice before either records it seen.
func runFetch(ctx context.Context, store fetchmail.Store, deliver fetchmail.Deliverer, interval time.Duration, logger *logging.Logger) {
	pollOnce(store, deliver, logger)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pollOnce(store, deliver, logger)
		}
	}
}

// pollOnce runs one fetch cycle, logging each failed account and the delivered count.
func pollOnce(store fetchmail.Store, deliver fetchmail.Deliverer, logger *logging.Logger) {
	n, errs := fetchmail.Poll(store, deliver, time.Now())
	for _, e := range errs {
		logger.Emit(logging.Event{
			Level:     logging.LevelError,
			Subsystem: logging.MTA,
			Name:      "fetchmail.error",
			Fields:    logging.Fields{"err": e.Error()},
		})
	}
	if n > 0 {
		logger.Info(logging.MTA, "fetchmail.delivered", logging.Fields{"count": n})
	}
}
