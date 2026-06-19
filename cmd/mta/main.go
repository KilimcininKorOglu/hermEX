// Command mta runs the hermEX SMTP intake daemon: it accepts mail and delivers
// it into recipient mailboxes resolved through the directory database.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"flag"
	"log"
	"net"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
	"hermex/internal/serve"
	"hermex/internal/smtp"
	"hermex/internal/spooler"
)

// senderOf returns the envelope sender for a released Outbox message: the
// address in its From header. The spooler hands the worker only the recipients
// and the raw message, so the return-path an out-of-office auto-reply targets is
// recovered from the message itself. An unparseable or missing From yields "",
// which the delivery path treats as a null return-path (no auto-reply).
func senderOf(raw []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	addrs, err := msg.Header.AddressList("From")
	if err != nil || len(addrs) == 0 {
		return ""
	}
	return addrs[0].Address
}

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
	if err := db.Ping(); err != nil {
		log.Fatalf("hermex-mta: directory unreachable: %v", err)
	}
	dir := directory.NewSQL(db)
	logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir, cfg.LogRetentionDays)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log

	// The outbound relay spool holds external recipients of authenticated
	// submissions until the relay worker delivers them. A single spool serves all
	// users; it lives under the data root alongside the mailbox stores.
	spool, err := relay.Open(filepath.Join(cfg.DataDir, "relay.sqlite3"))
	if err != nil {
		log.Fatalf("hermex-mta: open relay spool: %v", err)
	}

	addr := cfg.SMTPAddr
	if addr == "" {
		addr = ":25"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("hermex-mta: listen %s: %v", addr, err)
	}

	srv := &smtp.Server{Backend: &mta.Backend{Accounts: dir, Spool: spool, Logger: logger}, Hostname: cfg.Hostname, Logger: logger}
	if cfg.TLSEnabled() {
		tc, err := cfg.TLSConfig()
		if err != nil {
			log.Fatalf("hermex-mta: tls: %v", err)
		}
		srv.TLSConfig = tc // enables STARTTLS on the plaintext listener
	}
	srv.AddListener(ln)
	log.Printf("hermex-mta listening on %s", addr)

	// Optional implicit-TLS listener (e.g. :465) served alongside the plaintext
	// one; the stateless server handles both concurrently.
	if cfg.TLSEnabled() && cfg.SMTPSAddr != "" {
		tln, err := serve.TLSListener(cfg.SMTPSAddr, cfg)
		if err != nil {
			log.Fatalf("hermex-mta: implicit TLS on %s: %v", cfg.SMTPSAddr, err)
		}
		srv.AddListener(tln)
		log.Printf("hermex-mta listening on %s (implicit TLS)", cfg.SMTPSAddr)
	}

	// Release scheduled (send-later) messages from every mailbox's Outbox. This
	// runs in the always-on MTA so it survives webmail restarts. It is a lifecycle
	// component so shutdown cancels its loop alongside draining the SMTP server.
	deliver := func(recipients []string, raw []byte, when time.Time) ([]string, error) {
		return mta.Deliver(dir, senderOf(raw), recipients, raw, when)
	}
	slCtx, slCancel := context.WithCancel(context.Background())
	sendLater := lifecycle.Func{
		StartFn:    func() error { runSendLater(slCtx, dir, deliver, sendLaterInterval, logger); return nil },
		ShutdownFn: func(context.Context) error { slCancel(); return nil },
	}

	// Drain the outbound relay spool: deliver each authenticated submission's
	// external recipients to their mail exchangers, retrying transient failures.
	// Like the send-later sweep this is a single always-on loop, cancelled on
	// shutdown.
	relayWorker := &relay.Worker{
		Spool:    spool,
		HeloName: cfg.Hostname,
		Logger:   logger,
		// When the worker abandons an external recipient, return a non-delivery
		// report to the (local, authenticated) sender through the local delivery
		// path, so a failed send is reported rather than lost silently.
		OnGiveUp: func(it relay.Item, cause error) {
			report := mta.Bounce(it.From, it.Recipient, cause.Error(), time.Now())
			unresolved, err := mta.Deliver(dir, "", []string{it.From}, report, time.Now())
			if err != nil || len(unresolved) > 0 {
				logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "relay.bounce.undelivered", User: it.From, Fields: logging.Fields{"recipient": it.Recipient}})
			}
		},
	}
	rwCtx, rwCancel := context.WithCancel(context.Background())
	relayLoop := lifecycle.Func{
		StartFn:    func() error { relayWorker.Run(rwCtx, relayInterval); return nil },
		ShutdownFn: func(context.Context) error { rwCancel(); return nil },
	}

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "mta", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, []lifecycle.Component{srv, sendLater, relayLoop}, spool.Close, logClose, db.Close); err != nil {
		log.Fatalf("hermex-mta: %v", err)
	}
}

// sendLaterInterval is how often the worker scans every mailbox's Outbox for due
// scheduled sends. A scheduled message is released at most one interval late, so
// this bounds the send-time precision.
const sendLaterInterval = 30 * time.Second

// relayInterval is how often the relay worker scans the outbound spool. A freshly
// submitted external message waits at most this long for its first delivery
// attempt; deferred recipients wait for their own backoff regardless.
const relayInterval = 15 * time.Second

// runSendLater periodically sweeps every mailbox's Outbox, releasing scheduled
// sends whose time has come, until ctx is cancelled. Exactly one process must run
// this loop: a second concurrent sweeper could re-deliver a message in the window
// between its delivery and its removal from the Outbox, so it lives in the single
// always-on MTA daemon, not in the (possibly multi-instance, restartable) webmail.
func runSendLater(ctx context.Context, dir directory.MailboxLister, deliver spooler.DeliverFunc, interval time.Duration, logger *logging.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweepOutboxes(dir, deliver, logger)
		}
	}
}

// sweepOutboxes runs one pass: it opens each known mailbox and releases its due
// scheduled sends. Per-mailbox failures are logged and skipped so one bad
// mailbox cannot stall the rest.
func sweepOutboxes(dir directory.MailboxLister, deliver spooler.DeliverFunc, logger *logging.Logger) {
	maildirs, err := dir.Maildirs()
	if err != nil {
		log.Printf("hermex-mta send-later: list mailboxes: %v", err)
		return
	}
	for _, path := range maildirs {
		st, err := objectstore.Open(path)
		if err != nil {
			log.Printf("hermex-mta send-later: open %s: %v", path, err)
			continue
		}
		released, err := spooler.ProcessDueOutbox(st, deliver, time.Now())
		st.Close()
		if err != nil {
			log.Printf("hermex-mta send-later: %s: %v", path, err)
			logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "sendlater.error", Fields: logging.Fields{"mailbox": path}, Err: err.Error()})
		}
		if released > 0 {
			log.Printf("hermex-mta send-later: released %d scheduled message(s) from %s", released, path)
			logger.Info(logging.MTA, "sendlater.release", logging.Fields{"count": released, "mailbox": path})
		}
	}
}
