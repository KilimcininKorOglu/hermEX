// Command mta runs the hermEX SMTP intake daemon: it accepts mail and delivers
// it into recipient mailboxes resolved through the directory database.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"net/mail"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/antispam"
	"hermex/internal/authlimit"
	"hermex/internal/config"
	"hermex/internal/dane"
	"hermex/internal/directory"
	"hermex/internal/dkimsign"
	"hermex/internal/health"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/meeting"
	"hermex/internal/mta"
	"hermex/internal/mtasts"
	"hermex/internal/notify"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
	"hermex/internal/serve"
	"hermex/internal/smtp"
	"hermex/internal/spooler"
	"hermex/internal/tlscert"
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

// daemonName identifies this process in the central log and in every settings
// applier's failure record.
const daemonName = "hermex-mta"

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
	// At-rest wrapping for the private keys the directory stores (DKIM signing
	// keys, uploaded TLS keys). An unset secret leaves them in plaintext and says
	// so on startup.
	dir.SetKeySecret(cfg.KeyWrapSecret())
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-mta: schema: %v", err)
	}
	dir.SetLDAPVerifier(ldapauth.New())
	logger, logClose := logging.Build(daemonName, cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
	objectstore.SetDefaultLogger(logger) // store infra failures route to the central log
	mta.SetDefaultLogger(logger)         // post-delivery pass failures route to the central log

	// Push notifications: publish every delivery's mailbox write to the relay so a
	// recipient's parked notification long-poll (in another daemon) wakes the instant
	// the mail lands. A no-op when notify_url is empty.
	notify.EnableProducer(cfg.NotifyURL, cfg.NotifySecret, logger)

	// Antivirus: install the package-level scanner from clamd_addr (a no-op when
	// unset), so delivery scans inbound intake and authenticated submission.
	mta.EnableScanning(cfg.ClamdAddr, dir, cfg.QuarantinePath, cfg.Hostname, logger)

	// The outbound relay spool holds external recipients of authenticated
	// submissions until the relay worker delivers them. A single spool serves all
	// users; it lives under the data root alongside the mailbox stores.
	spool, err := relay.Open(cfg.RelaySpoolPath())
	if err != nil {
		log.Fatalf("hermex-mta: open relay spool: %v", err)
	}
	// DKIM-sign outbound mail with the sending domain's enabled key as it is spooled.
	spool.Signer = &dkimsign.Signer{Keys: dir, Logger: logger}

	// Automatic meeting-request processing runs at delivery for mailboxes configured
	// for it (resource rooms, auto-accepting users). Wired here, not in the mta
	// package, to break the meeting→mta import cycle. The organizer notification is
	// kept local-only (a nil spool): an internal organizer is notified, while an
	// external organizer is not, auto-relaying machine-generated replies to arbitrary
	// external addresses is a backscatter vector, gated separately like the
	// out-of-office reply.
	mta.OnMeetingRequest = func(st *objectstore.Store, accounts directory.Accounts, recipient string, msgID int64) bool {
		handled, err := meeting.AutoProcess(st, accounts, nil, recipient, msgID)
		if err != nil {
			log.Printf("hermex-mta: meeting auto-process for <%s>: %v", recipient, err)
		}
		return handled
	}
	// An inbound iTIP REPLY updates the organizer's calendar event so the
	// TrackingTab reflects attendee responses; best-effort, delivery-independent.
	mta.OnMeetingReply = func(st *objectstore.Store, sender string, msgID int64) bool {
		return meeting.ProcessReply(st, sender, msgID)
	}

	addr := cfg.SMTPAddr
	if addr == "" {
		addr = ":25"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("hermex-mta: listen %s: %v", addr, err)
	}

	scorer := antispam.New(antispam.DefaultWeights, antispam.DefaultThreshold)
	// Settings (weights, threshold, DNSBL zones) live in the database, seeded from
	// the built-in defaults on first run; the database is then the source of truth.
	if settings, err := loadAntispamSettings(dir); err != nil {
		log.Printf("hermex-mta: anti-spam settings load failed, using built-in defaults: %v", err)
	} else {
		scorer.SetConfig(antispamConfig(settings))
	}
	// The Bayesian model is loaded at startup, a data_dir model when present,
	// otherwise the embedded floor, and hot-reloaded after a retrain (below).
	model, err := antispam.LoadModel(cfg.DataDir)
	if err != nil {
		log.Printf("hermex-mta: anti-spam model load failed, using embedded floor: %v", err)
	}
	scorer.SetModel(model)
	// The SpamAssassin ruleset is seeded into data_dir on first run and loaded from
	// there at startup.
	rules, err := antispam.LoadRules(cfg.DataDir)
	if err != nil {
		log.Printf("hermex-mta: anti-spam ruleset load failed, using embedded baseline: %v", err)
	}
	scorer.SetRules(rules)
	// Operator allow/block rules override the verdict; loaded at startup and
	// hot-reloaded on change.
	if list, _, err := antispamAccess(dir); err != nil {
		log.Printf("hermex-mta: sender access rules load failed, none applied: %v", err)
	} else {
		scorer.SetAccess(list)
	}
	// Hot-reload edited settings, sender access rules, a refreshed ruleset, and a
	// retrained model so each takes effect without restarting the MTA, mail flow
	// never pauses.
	reloader := antispam.NewReloader(scorer, cfg.DataDir, log.Printf)
	reloader.WatchSettings(func() (*antispam.Config, int64, bool) {
		s, found, err := dir.GetAntispamSettings()
		if err != nil || !found {
			return nil, 0, false
		}
		return antispamConfig(s), s.UpdatedAt, true
	})
	reloader.WatchAccess(func() (*antispam.AccessList, uint64, bool) {
		list, h, err := antispamAccess(dir)
		if err != nil {
			return nil, 0, false
		}
		return list, h, true
	})
	go reloader.Run(context.Background(), time.Minute)
	// Greylisting defers a first-contact triplet so a legitimate MTA retries. It
	// starts disabled; the admin toggle is read at startup and hot-reloaded, and the
	// triplet table is pruned periodically to stay bounded.
	greylister := mta.NewGreylister(dir, scorer)
	applyGreylistSettings(dir, logger, greylister)
	go runGreylistMaintenance(dir, logger, greylister)
	// Inbound rate limiting caps how many messages an unauthenticated client network
	// may send per window. It starts disabled; the stored settings are read at startup
	// and hot-reloaded, and the window table is pruned periodically to stay bounded.
	rateLimiter := mta.NewRateLimiter()
	applyRateLimitSettings(dir, logger, rateLimiter)
	go runRateLimitMaintenance(dir, logger, rateLimiter)
	// Outbound abuse limiting caps how many external recipients a local account may
	// send to per window, a compromised account that blasts spam is deferred and the
	// admin is alerted. It starts disabled; the alert is a central-log event.
	outboundLimiter := mta.StartOutboundLimiter(daemonName, logger, dir.GetOutboundSettings)
	// Failed-login throttle on SMTP AUTH: a client address that piles up failed
	// logins is locked out for the window the operator configured, so submission
	// cannot be used to guess passwords unbounded.
	loginLimiter := authlimit.New(0, 0, 0)
	authlimit.Apply(daemonName, logger, loginLimiter, dir.GetLoginLockoutSettings)
	go authlimit.RunMaintenance(daemonName, logger, loginLimiter, dir.GetLoginLockoutSettings)

	// Wire delivery-time inbox-rule forwarding to the relay spool, gated by the
	// outbound abuse limiter (the per-user cap). Wired here, not in the mta package,
	// to keep the store free of any send dependency (like OnMeetingRequest). The
	// envelope sender is the forwarding owner so bounces return to them and the relay
	// DKIM path signs for their domain; the loop/backscatter guards already ran.
	mta.OnRuleForward = ruleHook("forward", outboundLimiter, spool.Enqueue, logger)
	// Reject bounces and vacation auto-replies a delivery-time inbox rule generated:
	// enqueued from the owning mailbox (DKIM domain) under the same outbound cap. The
	// store built the bytes and applied the backscatter/loop guards.
	mta.OnRuleSend = ruleHook("send", outboundLimiter, spool.Enqueue, logger)
	// Spam-history retention: how many of the most recent scored verdicts the
	// spam_history table keeps. It is read at startup and re-read every minute so an
	// admin's change applies without a restart.
	applySpamHistorySettings(dir, logger)
	go runSpamHistoryMaintenance(dir, logger)
	// The three background loops in this daemon must each run in one process at a
	// time, and none of them leases the work it picks up: a second sweeper
	// re-delivers a scheduled message, a second drainer re-delivers a spooled
	// recipient, a second digest pass re-reads a watermark its sibling has not
	// advanced yet and mails the summary twice. A named advisory lock in the shared
	// directory database enforces it across instances. It is taken per pass, so an
	// instance that dies mid-pass drops its connection, the server frees the lock,
	// and another instance takes over on its next tick.
	lockPass := func(name string) func() (func(), bool) {
		return func() (func(), bool) {
			release, ok, err := dir.TryLock(context.Background(), name)
			if err != nil {
				// Do not proceed unguarded: a directory that cannot answer is also a
				// directory that cannot deliver, and running anyway risks duplicates.
				logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "worker.lock.fail",
					Fields: logging.Fields{"lock": name}, Err: err.Error()})
				return nil, false
			}
			return release, ok
		}
	}
	// Quarantine digest: deliver each user a periodic summary of newly quarantined
	// mail with signed one-click release links. It needs a shared signing secret (the
	// webmail release endpoint verifies the same key); without one nothing can be
	// sent, since every entry carries a signed link.
	//
	// The worker runs either way. Gating its startup on the secret made the failure
	// silent: an operator who turned the digest on without the key was told it was
	// active by a panel reading the toggle alone, and no goroutine existed to
	// report otherwise. It now starts, sees the same toggle, and says on every run
	// that it cannot send.
	go runDigest(dir, []byte(cfg.DigestSecret), cfg.Hostname, lockPass(directory.LockDigest), logger)
	srv := &smtp.Server{Backend: &mta.Backend{Accounts: dir, Spool: spool, Logger: logger, Scorer: scorer, History: dir, Greylist: greylister, RateLimit: rateLimiter, Thresholds: dir, RecipientAccess: dir, Outbound: outboundLimiter, Limiter: loginLimiter}, Hostname: cfg.Hostname, Logger: logger}
	// TLS certificates come from the provider: the config-file cert as a fallback,
	// overridden by an admin-uploaded cert the provider polls for, so a renewal
	// applies without a restart.
	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		log.Fatalf("hermex-mta: tls: %v", err)
	}
	if provider.TLSEnabled() {
		tc, _ := provider.TLSConfig()
		srv.TLSConfig = tc // enables STARTTLS on the plaintext listener
		go provider.RunMaintenance()
	}
	// Inbound message size limit: the max bytes the SMTP server accepts and advertises
	// (SMTP SIZE). Read at startup and re-read every minute so an admin's change applies
	// without a restart; 0 means no limit.
	applyMessageSizeSettings(dir, logger, srv)
	go runMessageSizeMaintenance(dir, logger, srv)
	// The same limit on the paths that never reach an SMTP session: the send-later
	// release, and the meeting replies this daemon files.
	mta.StartMessageSizeLimit(daemonName, logger, dir.GetMessageSizeSettings)
	srv.AddListener(ln)
	log.Printf("hermex-mta listening on %s", addr)

	// Optional implicit-TLS listener (e.g. :465) served alongside the plaintext
	// one; the stateless server handles both concurrently.
	if provider.TLSEnabled() && cfg.SMTPSAddr != "" {
		tln, err := serve.TLSListener(cfg.SMTPSAddr, provider)
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
		return mta.DeliverAndRelay(dir, spool, senderOf(raw), recipients, raw, when)
	}
	// When the spooler abandons a scheduled send it moves the message back to
	// Drafts; tell the sender why, the same way the relay worker reports an
	// abandoned external recipient. One report per recipient, so each carries a
	// well-formed Final-Recipient.
	onGiveUp := func(raw []byte, recipients []string, cause error) {
		from := senderOf(raw)
		logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "sendlater.giveup",
			User: from, Fields: logging.Fields{"recipients": len(recipients)}, Err: cause.Error()})
		if from == "" {
			return
		}
		for _, rcpt := range recipients {
			report := mta.Bounce(cfg.Hostname, from, rcpt, cause.Error(), time.Now())
			unresolved, err := mta.Deliver(dir, "", []string{from}, report, time.Now())
			if err != nil || len(unresolved) > 0 {
				logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "sendlater.bounce.undelivered", User: from, Fields: logging.Fields{"recipient": rcpt}})
			}
		}
	}
	// lifecycle.Loop, not lifecycle.Func: shutdown must wait for the sweep to
	// return, because the cleanups that follow close the spool and the directory
	// database this loop delivers through.
	sendLater := lifecycle.Loop(func(ctx context.Context) {
		runSendLater(ctx, dir, deliver, onGiveUp, lockPass(directory.LockSendLater), sendLaterInterval, logger)
	})

	// Drain the outbound relay spool: deliver each authenticated submission's
	// external recipients to their mail exchangers, retrying transient failures.
	// Like the send-later sweep this is a single always-on loop, cancelled on
	// shutdown.
	relayWorker := &relay.Worker{
		Spool:    spool,
		HeloName: cfg.Hostname,
		Logger:   logger,
		Guard:    lockPass(directory.LockRelayDrain),
		// Honor recipients' published MTA-STS policies (RFC 8461): a domain in
		// enforce mode gets validated TLS to a policy-listed MX or no delivery. This
		// only changes behavior for domains that opt in by publishing a policy.
		Policy: (&mtasts.Resolver{}).Lookup,
		// DANE/TLSA (RFC 7672): when an operator configures a DNSSEC-validating
		// resolver, authenticate outbound TLS against MX hosts' TLSA records.
		// Empty leaves DANE off, so delivery stays on opportunistic TLS + MTA-STS.
		DANE: daneResolver(cfg.DaneResolver),
		// TLS-RPT (RFC 8460): record each outbound TLS session outcome so the spool
		// can build the recipient domain's daily aggregate report. The spool is the
		// store, so it doubles as the reporter.
		TLSReporter: spool,
		// When the worker abandons an external recipient, return a non-delivery
		// report to the (local, authenticated) sender through the local delivery
		// path, so a failed send is reported rather than lost silently.
		OnGiveUp: func(it relay.Item, cause error) error {
			// RFC 3461: honor the recipient's NOTIFY. NEVER (or any value not
			// requesting FAILURE) means the sender wants no failure notice, so
			// suppress the bounce rather than emit backscatter. Reported as success:
			// nothing was lost, the sender asked not to be told.
			if !mta.NotifyFailureWanted(it.Notify) {
				return nil
			}
			report := mta.Bounce(cfg.Hostname, it.From, it.Recipient, cause.Error(), time.Now())
			unresolved, err := mta.Deliver(dir, "", []string{it.From}, report, time.Now())
			if err == nil && len(unresolved) == 0 {
				return nil
			}
			logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "relay.bounce.undelivered", User: it.From, Fields: logging.Fields{"recipient": it.Recipient}})
			if err != nil {
				return err
			}
			return fmt.Errorf("bounce sender %q does not resolve", it.From)
		},
	}
	// Outbound delivery retry policy (base backoff and max attempts): read at startup
	// and re-read every minute so an admin's change applies without a restart.
	applyRelaySettings(dir, logger, relayWorker)
	go runRelayMaintenance(dir, logger, relayWorker)
	// Joined on shutdown for the same reason as the send-later sweep: an in-flight
	// pass that settles a delivered recipient must finish before spool.Close runs,
	// or the settle fails and the next start re-delivers an already-sent message.
	relayLoop := lifecycle.Loop(func(ctx context.Context) { relayWorker.Run(ctx, relayInterval) })

	logger.Info(logging.System, "daemon.startup", logging.Fields{"daemon": "mta", "addr": addr})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider.TLSEnabled() {
		// Report the serving certificate's remaining validity, so a renewal that
		// failed shows as degraded before clients start failing handshakes.
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	comps := append([]lifecycle.Component{srv, sendLater, relayLoop},
		health.Components(cfg.HealthAddr, "mta", checks...)...)
	if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, spool.Close, logClose, db.Close); err != nil {
		log.Fatalf("hermex-mta: %v", err)
	}
}

// antispamConfig maps stored anti-spam settings to the scorer's Config.
func antispamConfig(s directory.AntispamSettings) *antispam.Config {
	return &antispam.Config{
		Weights: antispam.Weights{
			SPFFail: s.SPFFail, SPFSoftFail: s.SPFSoftFail, DKIMFail: s.DKIMFail,
			DMARCFail: s.DMARCFail, DNSBLHit: s.DNSBLHit, BayesSpam: s.BayesSpam, SARulesHit: s.SARulesHit,
		},
		Threshold:   s.Threshold,
		Zones:       antispam.ParseZones(s.Zones),
		BayesProb:   s.BayesProb,
		SAThreshold: s.SAThreshold,
	}
}

// loadAntispamSettings returns the stored settings, seeding the built-in defaults
// (with the HERMEX_DNSBL_ZONES env value migrated in once) on first run so the
// database becomes the single source of truth thereafter.
func loadAntispamSettings(dir *directory.SQLDirectory) (directory.AntispamSettings, error) {
	s, found, err := dir.GetAntispamSettings()
	if err != nil {
		return directory.AntispamSettings{}, err
	}
	if found {
		return s, nil
	}
	w := antispam.DefaultWeights
	seed := directory.AntispamSettings{
		SPFFail: w.SPFFail, SPFSoftFail: w.SPFSoftFail, DKIMFail: w.DKIMFail, DMARCFail: w.DMARCFail,
		DNSBLHit: w.DNSBLHit, BayesSpam: w.BayesSpam, SARulesHit: w.SARulesHit,
		Threshold: antispam.DefaultThreshold, Zones: os.Getenv("HERMEX_DNSBL_ZONES"),
		BayesProb: antispam.DefaultBayesProb, SAThreshold: antispam.DefaultSAThreshold,
	}
	if err := dir.SetAntispamSettings(seed); err != nil {
		return seed, err
	}
	s, _, err = dir.GetAntispamSettings() // re-read to pick up the stamped version
	return s, err
}

// antispamAccess loads the sender allow/block rules into an antispam.AccessList and
// returns a content hash of them, the version the reloader compares to detect a
// change (a hash, not a counter, so a delete is caught too).
func antispamAccess(dir *directory.SQLDirectory) (*antispam.AccessList, uint64, error) {
	rules, err := dir.ListSenderRules()
	if err != nil {
		return nil, 0, err
	}
	m := make(map[string]string, len(rules))
	for _, r := range rules {
		m[r.Pattern] = r.Action
	}
	return antispam.NewAccessList(m), accessHash(rules), nil
}

// enqueueFunc is the spool entry point a rule hook puts mail on: (*relay.Spool).Enqueue.
type enqueueFunc func(from string, to []string, raw []byte, now time.Time) error

// ruleHook builds the hook a delivery-time inbox rule calls to put mail on the wire,
// either a forward or a rule-generated bounce/vacation reply (kind names which). Both
// outcomes it can fail with are silent to the user whose rule it is: the outbound cap
// defers the mail, or the spool refuses it. Neither can fail the delivery that
// triggered the rule, which is exactly why both are recorded, an operator asked "why
// did my forwarding rule not fire" has nothing else to go on.
func ruleHook(kind string, limiter *mta.OutboundLimiter, enqueue enqueueFunc, logger *logging.Logger) func(string, []string, []byte) {
	return func(owner string, to []string, raw []byte) {
		if !limiter.Allow(owner) {
			logger.Emit(logging.Event{Level: logging.LevelWarn, Subsystem: logging.MTA,
				Name: "rule." + kind + ".deferred", User: owner,
				Fields: logging.Fields{"recipients": len(to), "detail": "the outbound cap was reached"}})
			return
		}
		if err := enqueue(owner, to, raw, time.Now()); err != nil {
			logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA,
				Name: "rule." + kind + ".enqueue", User: owner,
				Fields: logging.Fields{"recipients": len(to)}, Err: err.Error()})
		}
	}
}

// accessHash folds the rules (already returned in a deterministic order) into a
// content hash so any add, edit, or delete changes the value.
func accessHash(rules []directory.SenderRule) uint64 {
	h := fnv.New64a()
	for _, r := range rules {
		h.Write([]byte(r.Pattern))
		h.Write([]byte{0})
		h.Write([]byte(r.Action))
		h.Write([]byte{'\n'})
	}
	return h.Sum64()
}

// applyGreylistSettings reads the stored greylist on/off toggle and timings and
// applies both to the greylister. A read error leaves that part unchanged, so a
// transient failure never flips greylisting or resets a timing; a missing timings row
// keeps the greylister's built-in defaults.
func applyGreylistSettings(dir *directory.SQLDirectory, logger *logging.Logger, g *mta.Greylister) {
	if on, err := dir.GetGreylistEnabled(); err != nil {
		logging.SettingsReadFailed(logger, daemonName, "greylist-toggle", "leaving it unchanged", err)
	} else {
		g.SetEnabled(on)
	}
	if t, found, err := dir.GetGreylistTimings(); err != nil {
		logging.SettingsReadFailed(logger, daemonName, "greylist-timings", "leaving them unchanged", err)
	} else if found {
		g.SetTimings(t.MinDelay, t.UnconfirmedTTL, t.ConfirmedTTL)
	}
}

// runGreylistMaintenance hot-reloads the greylist toggle and timings every minute and
// prunes the expired triplets hourly, so an admin change applies without a restart
// and the table stays bounded. It runs until the process exits.
func runGreylistMaintenance(dir *directory.SQLDirectory, logger *logging.Logger, g *mta.Greylister) {
	applyTick := time.NewTicker(time.Minute)
	pruneTick := time.NewTicker(time.Hour)
	defer applyTick.Stop()
	defer pruneTick.Stop()
	for {
		select {
		case <-applyTick.C:
			applyGreylistSettings(dir, logger, g)
		case <-pruneTick.C:
			if err := g.Prune(); err != nil {
				log.Printf("hermex-mta: greylist prune failed: %v", err)
			}
		}
	}
}

// applyRateLimitSettings reads the stored rate-limit settings and applies them to the
// limiter. A missing row or a read error leaves the limiter at its defaults, so a
// settings failure never starts throttling unexpectedly; a transient read error keeps
// the last applied setting rather than flipping the limiter off.
func applyRateLimitSettings(dir *directory.SQLDirectory, logger *logging.Logger, rl *mta.RateLimiter) {
	s, found, err := dir.GetRateLimitSettings()
	if err != nil {
		logging.SettingsReadFailed(logger, daemonName, "inbound-rate-limit", "leaving rate limiting unchanged", err)
		return
	}
	if !found {
		return
	}
	rl.SetLimits(s.Burst, time.Duration(s.WindowSeconds)*time.Second)
	rl.SetEnabled(s.Enabled)
}

// runRateLimitMaintenance re-applies the rate-limit settings every minute so an admin
// change takes effect without a restart, and prunes the limiter's window table hourly
// to keep it bounded.
func runRateLimitMaintenance(dir *directory.SQLDirectory, logger *logging.Logger, rl *mta.RateLimiter) {
	applyTick := time.NewTicker(time.Minute)
	pruneTick := time.NewTicker(time.Hour)
	defer applyTick.Stop()
	defer pruneTick.Stop()
	for {
		select {
		case <-applyTick.C:
			applyRateLimitSettings(dir, logger, rl)
		case <-pruneTick.C:
			rl.Prune()
		}
	}
}

// applySpamHistorySettings reads the stored spam-history retention and applies it to
// the directory's runtime bound. A missing row or a read error leaves the bound
// unchanged, so a settings failure never shrinks the history unexpectedly. Pruning
// itself happens per-insert in RecordSpamVerdict, so this only re-reads the bound.
func applySpamHistorySettings(dir *directory.SQLDirectory, logger *logging.Logger) {
	s, found, err := dir.GetSpamHistorySettings()
	if err != nil {
		logging.SettingsReadFailed(logger, daemonName, "spam-history", "leaving the retention unchanged", err)
		return
	}
	if !found {
		return
	}
	dir.SetSpamHistoryRetain(int64(s.Retain))
}

// runSpamHistoryMaintenance re-applies the spam-history retention every minute so an
// admin change takes effect without a restart. It runs until the process exits.
func runSpamHistoryMaintenance(dir *directory.SQLDirectory, logger *logging.Logger) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applySpamHistorySettings(dir, logger)
	}
}

// applyMessageSizeSettings reads the stored inbound message size limit and applies it
// to the SMTP server. A missing row or a read error leaves the limit unchanged, so a
// settings failure never starts rejecting mail unexpectedly.
func applyMessageSizeSettings(dir *directory.SQLDirectory, logger *logging.Logger, srv *smtp.Server) {
	s, found, err := dir.GetMessageSizeSettings()
	if err != nil {
		logging.SettingsReadFailed(logger, daemonName, "message-size", "leaving the limit unchanged", err)
		return
	}
	if !found {
		return
	}
	srv.SetMaxSize(s.MaxInboundBytes)
}

// runMessageSizeMaintenance re-applies the inbound message size limit every minute so
// an admin change takes effect without a restart. It runs until the process exits.
func runMessageSizeMaintenance(dir *directory.SQLDirectory, logger *logging.Logger, srv *smtp.Server) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyMessageSizeSettings(dir, logger, srv)
	}
}

// daneResolver builds the relay worker's DANE resolver from the configured
// validating-resolver address. An empty address returns nil, leaving DANE off so
// outbound delivery stays on opportunistic TLS plus any MTA-STS policy.
func daneResolver(addr string) *dane.Resolver {
	if addr == "" {
		return nil
	}
	return &dane.Resolver{Addr: addr}
}

// applyRelaySettings reads the stored outbound retry policy and applies it to the relay
// worker. A missing row or a read error leaves the policy unchanged, so a settings
// failure never alters delivery behavior unexpectedly.
func applyRelaySettings(dir *directory.SQLDirectory, logger *logging.Logger, w *relay.Worker) {
	s, found, err := dir.GetRelaySettings()
	if err != nil {
		logging.SettingsReadFailed(logger, daemonName, "relay", "leaving the retry policy unchanged", err)
		return
	}
	if !found {
		return
	}
	w.SetRetryPolicy(time.Duration(s.BackoffSeconds)*time.Second, s.MaxAttempts)
}

// runRelayMaintenance re-applies the outbound retry policy every minute so an admin
// change takes effect without a restart. It runs until the process exits.
func runRelayMaintenance(dir *directory.SQLDirectory, logger *logging.Logger, w *relay.Worker) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyRelaySettings(dir, logger, w)
	}
}

// runDigest delivers the quarantine digest on the configured cadence. It checks the
// stored settings hourly and runs a pass when the digest is enabled and at least the
// configured interval has elapsed since the last; the per-mailbox watermark keeps each
// pass to mail that arrived since that mailbox's last summary. Release links are valid
// for the interval plus a week's grace so a user has time to act before they expire.
func runDigest(dir *directory.SQLDirectory, secret []byte, hostname string, guard func() (func(), bool), logger *logging.Logger) {
	const checkEvery = time.Hour
	const grace = 7 * 24 * time.Hour
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	var lastRun time.Time
	for range t.C {
		s, found, err := dir.GetDigestSettings()
		if err != nil || !found || !s.Enabled {
			continue
		}
		if !digestCanSend(secret, logger) {
			continue
		}
		interval := time.Duration(s.IntervalHours) * time.Hour
		if interval <= 0 {
			interval = 24 * time.Hour
		}
		if !lastRun.IsZero() && time.Since(lastRun) < interval {
			continue
		}
		runner := &mta.DigestRunner{
			Dir: dir, Secret: secret, BaseURL: s.BaseURL, Hostname: hostname,
			TokenTTL: interval + grace, Now: time.Now, Logger: logger,
		}
		n, ran := digestOnce(runner, guard)
		if !ran {
			continue // another instance is running this pass; retry next tick
		}
		lastRun = time.Now()
		logger.Info(logging.MTA, "digest.run", logging.Fields{"sent": n})
	}
}

// digestOnce runs one digest pass while holding the pass lock, reporting how many
// digests went out and whether the pass ran at all. Each mailbox's watermark is
// advanced only after its digest is delivered, so two overlapping passes both read
// the pre-advance watermark and each mails a full summary carrying its own valid
// release links. Refusing the pass (another instance holds the lock, or the
// directory could not answer) leaves lastRun alone so this instance retries.
func digestOnce(runner *mta.DigestRunner, guard func() (func(), bool)) (int, bool) {
	release, ok := guard()
	if !ok {
		return 0, false
	}
	defer release()
	return runner.Run(), true
}

// digestCanSend reports whether an enabled digest can actually produce anything,
// and says why not when it cannot.
//
// Every entry in a summary carries a signed one-click release link, so with no
// signing secret there is nothing to send, however the toggle reads. Reporting it
// on every run is the point: the failure is otherwise perfectly silent, and
// quarantined legitimate mail sits unnoticed for exactly as long as nobody knows
// the summaries stopped arriving.
func digestCanSend(secret []byte, logger *logging.Logger) bool {
	if len(secret) > 0 {
		return true
	}
	logger.Emit(logging.Event{
		Level: logging.LevelWarn, Subsystem: logging.MTA, Name: "digest.disabled",
		Err: "the quarantine digest is enabled but no digest_secret is configured, so no summary can be signed or sent",
	})
	return false
}

// sendLaterInterval is how often the worker scans every mailbox's Outbox for due
// scheduled sends. A scheduled message is released at most one interval late, so
// this bounds the send-time precision.
const sendLaterInterval = 30 * time.Second

// perMailboxSweepBudget bounds how long one mailbox may hold the send-later
// sweep. The sweep walks mailboxes in order, so without a budget a single slow
// one, a store on failing disk, a long queue of messages each waiting on the virus
// scanner, consumes the whole pass and every mailbox behind it waits for the next
// one, and the next. With the budget each mailbox is served in every pass.
//
// It bounds the number of messages a mailbox gets through, not a call already in
// flight: the delivery function takes no context, so the message being released
// when the budget expires still finishes. The one network hop inside a delivery,
// the virus scan, carries its own 30-second deadline.
// It is a variable so a test can shrink it; nothing else assigns to it.
var perMailboxSweepBudget = sendLaterInterval

// relayInterval is how often the relay worker scans the outbound spool. A freshly
// submitted external message waits at most this long for its first delivery
// attempt; deferred recipients wait for their own backoff regardless.
const relayInterval = 15 * time.Second

// runSendLater periodically sweeps every mailbox's Outbox, releasing scheduled
// sends whose time has come, until ctx is cancelled. Exactly one process may
// sweep at a time: a second concurrent sweeper could re-deliver a message in the
// window between its delivery and its removal from the Outbox. guard enforces
// that across instances, and a process it refuses simply waits for the next tick.
// A nil guard runs every sweep, the single-instance behaviour.
func runSendLater(ctx context.Context, dir directory.MailboxLister, deliver spooler.DeliverFunc, onGiveUp spooler.GiveUpFunc, guard func() (func(), bool), interval time.Duration, logger *logging.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			guardedSweep(ctx, dir, deliver, onGiveUp, guard, logger)
		}
	}
}

// guardedSweep runs one sweep while holding the guard's permission, and skips the
// sweep entirely when the guard refuses, because another instance is sweeping the
// same mailboxes right now.
func guardedSweep(ctx context.Context, dir directory.MailboxLister, deliver spooler.DeliverFunc, onGiveUp spooler.GiveUpFunc, guard func() (func(), bool), logger *logging.Logger) {
	if guard != nil {
		release, ok := guard()
		if !ok {
			return
		}
		defer release()
	}
	sweepOutboxes(ctx, dir, deliver, onGiveUp, logger)
}

// sweepOutboxes runs one pass: it opens each known mailbox and releases its due
// scheduled sends. Per-mailbox failures are logged and skipped so one bad
// mailbox cannot stall the rest.
func sweepOutboxes(ctx context.Context, dir directory.MailboxLister, deliver spooler.DeliverFunc, onGiveUp spooler.GiveUpFunc, logger *logging.Logger) {
	maildirs, err := dir.Maildirs()
	if err != nil {
		log.Printf("hermex-mta send-later: list mailboxes: %v", err)
		return
	}
	var total spooler.Stats
	mailboxesFailed, mailboxesOverBudget := 0, 0
	for _, path := range maildirs {
		// Stop between mailboxes on shutdown. ProcessDueOutbox already returns at
		// once when cancelled, but without this the sweep would still open and close
		// a store for every remaining mailbox, which on a large deployment is the
		// difference between a prompt drain and one that outlasts the deadline.
		if ctx.Err() != nil {
			return
		}
		st, err := objectstore.Open(path)
		if err != nil {
			log.Printf("hermex-mta send-later: open %s: %v", path, err)
			continue
		}
		mbCtx, cancelMailbox := context.WithTimeout(ctx, perMailboxSweepBudget)
		stats, err := spooler.ProcessDueOutboxStats(mbCtx, st, deliver, onGiveUp, time.Now())
		budgetSpent := mbCtx.Err() != nil && ctx.Err() == nil
		cancelMailbox()
		st.Close()
		if budgetSpent {
			mailboxesOverBudget++
			logger.Emit(logging.Event{Level: logging.LevelWarn, Subsystem: logging.MTA,
				Name: "sendlater.budget", Fields: logging.Fields{"mailbox": path, "released": stats.Released}})
		}
		total.Scanned += stats.Scanned
		total.Released += stats.Released
		total.Failed += stats.Failed
		total.Waiting += stats.Waiting
		total.Retrying += stats.Retrying
		if err != nil {
			mailboxesFailed++
			log.Printf("hermex-mta send-later: %s: %v", path, err)
			logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "sendlater.error", Fields: logging.Fields{"mailbox": path}, Err: err.Error()})
		}
		if stats.Released > 0 {
			log.Printf("hermex-mta send-later: released %d scheduled message(s) from %s", stats.Released, path)
			logger.Info(logging.MTA, "sendlater.release", logging.Fields{"count": stats.Released, "mailbox": path})
		}
	}
	// One summary per sweep. The per-mailbox lines above only appear when something
	// happened, so without this a backlog that is merely growing, rather than
	// failing, leaves no trace at all: nothing is released and nothing errors while
	// the queue fills. Waiting is the depth reading, retrying is where a stuck send
	// shows up before it exhausts its budget.
	level := logging.LevelInfo
	if mailboxesFailed > 0 || mailboxesOverBudget > 0 {
		level = logging.LevelWarn
	}
	logger.Emit(logging.Event{
		Level: level, Subsystem: logging.MTA, Name: "sendlater.sweep",
		Fields: logging.Fields{
			"mailboxes": len(maildirs), "mailboxes_failed": mailboxesFailed,
			"mailboxes_over_budget": mailboxesOverBudget,
			"scanned":               total.Scanned, "released": total.Released, "failed": total.Failed,
			"waiting": total.Waiting, "retrying": total.Retrying,
		},
	})
}
