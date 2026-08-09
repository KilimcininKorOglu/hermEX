// Command admin provisions the hermEX directory: it ensures the schema and
// creates domains, users, and aliases in the directory database, and serves the
// admin API.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/admin"
	"hermex/internal/authlimit"
	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/health"
	"hermex/internal/httplimit"
	"hermex/internal/ldapauth"
	"hermex/internal/ldapsync"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/serve"
	"hermex/internal/tlscert"
)

// The admin server consumes the directory through its own interface; this proves
// the concrete *SQLDirectory satisfies it.
var _ admin.Directory = (*directory.SQLDirectory)(nil)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hermex-admin -config <file> <command> [args]")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  ensure-schema")
	fmt.Fprintln(os.Stderr, "  create-domain <domainname>")
	fmt.Fprintln(os.Stderr, "  create-user <email> <password>")
	fmt.Fprintln(os.Stderr, "  create-alias <alias-address> <user-email>")
	fmt.Fprintln(os.Stderr, "  create-contact <email> <domain> [display-name]   (an org mail contact in the GAL)")
	fmt.Fprintln(os.Stderr, "  update-contact <email> <display-name>   (rename; an empty name clears it)")
	fmt.Fprintln(os.Stderr, "  delete-contact <email>")
	fmt.Fprintln(os.Stderr, "  list-contacts")
	fmt.Fprintln(os.Stderr, "  sweep-content <email>   (reclaim orphan content files; refuses while the mailbox is in use)")
	fmt.Fprintln(os.Stderr, "  prune-eml <email|all> [days]   (reclaim cached wire copies older than N days, default 30)")
	fmt.Fprintln(os.Stderr, "  backup-mail <email|all> <dest-dir>   (consistent copy of mail content; safe on a live mailbox)")
	fmt.Fprintln(os.Stderr, "  export-dkim <domain>    (write the domain's DKIM private key to stdout)")
	fmt.Fprintln(os.Stderr, "  ldap-sync <org-id>      (import the org's LDAP/AD accounts into the directory)")
	fmt.Fprintln(os.Stderr, "  grant-admin <email> <system|org|domain> [scope-id]")
	fmt.Fprintln(os.Stderr, "  list-sessions <email>   (the account's live webmail and panel sessions)")
	fmt.Fprintln(os.Stderr, "  revoke-sessions <email> (end every one of them; compromise response)")
	fmt.Fprintln(os.Stderr, "  serve                   (run the admin API HTTP server)")
	os.Exit(2)
}

func main() {
	cfgPath := flag.String("config", "/etc/hermex/config.json", "path to the JSON config file")
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		usage()
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("hermex-admin: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("hermex-admin: %v", err)
	}
	defer db.Close()
	dir := directory.NewSQL(db)
	// At-rest wrapping for the private keys the directory stores (DKIM signing
	// keys, uploaded TLS keys). An unset secret leaves them in plaintext and says
	// so on startup.
	dir.SetKeySecret(cfg.KeyWrapSecret())
	if err := dir.EnsureSchema(); err != nil {
		log.Fatalf("hermex-admin: schema: %v", err)
	}

	switch args[0] {
	case "ensure-schema":
		fmt.Println("schema ensured")
	case "create-domain":
		if len(args) != 2 {
			usage()
		}
		if _, err := dir.CreateDomain(args[1], cfg.HomedirFor(args[1])); err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
		fmt.Printf("domain %s created\n", args[1])
	case "create-user":
		if len(args) != 3 {
			usage()
		}
		if _, err := dir.CreateUser(args[1], args[2], cfg.MaildirFor(args[1])); err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
		fmt.Printf("user %s created\n", args[1])
	case "create-alias":
		if len(args) != 3 {
			usage()
		}
		if err := dir.CreateAlias(args[1], args[2]); err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
		fmt.Printf("alias %s -> %s created\n", args[1], args[2])
	case "create-contact":
		if len(args) < 3 || len(args) > 4 {
			usage()
		}
		name := ""
		if len(args) == 4 {
			name = args[3]
		}
		if _, err := dir.CreateContact(args[1], name, args[2]); err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
		fmt.Printf("contact %s created\n", args[1])
	case "update-contact":
		if len(args) != 3 {
			usage()
		}
		found, err := dir.UpdateContact(args[1], args[2])
		if err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
		if !found {
			log.Fatalf("hermex-admin: no such contact: %s", args[1])
		}
		fmt.Printf("contact %s updated\n", args[1])
	case "delete-contact":
		if len(args) != 2 {
			usage()
		}
		removed, err := dir.DeleteContact(args[1])
		if err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
		if !removed {
			log.Fatalf("hermex-admin: no such contact: %s", args[1])
		}
		fmt.Printf("contact %s deleted\n", args[1])
	case "list-contacts":
		contacts, err := dir.ListContacts()
		if err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
		for _, c := range contacts {
			fmt.Printf("%s\t%s\t%s\n", c.Address, c.DisplayName, c.Domain)
		}
	case "sweep-content":
		if len(args) != 2 {
			usage()
		}
		maildir, ok := dir.Resolve(args[1])
		if !ok {
			log.Fatalf("hermex-admin: unknown or unreceivable mailbox: %s", args[1])
		}
		store, err := objectstore.Open(maildir)
		if err != nil {
			log.Fatalf("hermex-admin: open mailbox: %v", err)
		}
		defer store.Close()
		removed, err := store.SweepOrphanContent()
		if errors.Is(err, objectstore.ErrMailboxBusy) {
			// The sweep deletes content a live writer may be about to reference, so
			// it declines rather than doing it anyway. Say which mailbox and what to
			// do, since "busy" alone leaves the operator guessing.
			log.Fatalf("hermex-admin: %s is open by a running daemon or another session; "+
				"stop the mail services for this mailbox and retry", args[1])
		}
		if err != nil {
			log.Fatalf("hermex-admin: sweep: %v", err)
		}
		fmt.Printf("swept %d orphan content file(s) from %s\n", removed, args[1])
	case "prune-eml":
		if len(args) < 2 || len(args) > 3 {
			usage()
		}
		days := defaultEMLPruneDays
		if len(args) == 3 {
			if days, err = strconv.Atoi(args[2]); err != nil || days < 0 {
				log.Fatalf("hermex-admin: days %q: want a non-negative whole number", args[2])
			}
		}
		pruneEML(dir, args[1], days)
	case "backup-mail":
		if len(args) != 3 {
			usage()
		}
		backupMail(dir, cfg, args[1], args[2])
	case "export-dkim":
		if len(args) != 2 {
			usage()
		}
		exportDKIM(dir, args[1])
	case "ldap-sync":
		if len(args) != 2 {
			usage()
		}
		orgID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			log.Fatalf("hermex-admin: org id %q: %v", args[1], err)
		}
		lcfg, ok, err := dir.GetLDAPConfig(orgID)
		if err != nil {
			log.Fatalf("hermex-admin: ldap config: %v", err)
		}
		if !ok {
			log.Fatalf("hermex-admin: organization %d has no LDAP configuration", orgID)
		}
		summary, err := ldapsync.Run(lcfg, ldapauth.New(), dir, cfg.MaildirFor,
			func(f string, a ...any) { log.Printf("hermex-admin: "+f, a...) })
		if err != nil {
			log.Fatalf("hermex-admin: ldap sync: %v", err)
		}
		fmt.Printf("ldap-sync org %d: %s\n", orgID, summary)
	case "grant-admin":
		if len(args) < 3 || len(args) > 4 {
			usage()
		}
		uid, ok, err := dir.UserID(args[1])
		if err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
		if !ok {
			log.Fatalf("hermex-admin: unknown user: %s", args[1])
		}
		var scope int64
		if len(args) == 4 {
			if scope, err = strconv.ParseInt(args[3], 10, 64); err != nil {
				log.Fatalf("hermex-admin: scope id %q: %v", args[3], err)
			}
		}
		if err := dir.GrantAdminRole(uid, args[2], scope); err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
		fmt.Printf("granted %s the %s admin role (scope %d)\n", args[1], args[2], scope)
	case "list-sessions":
		if len(args) != 2 {
			usage()
		}
		listSessions(dir, args[1])
	case "revoke-sessions":
		if len(args) != 2 {
			usage()
		}
		revokeSessions(dir, args[1])
	case "serve":
		if cfg.AdminSecret == "" {
			log.Fatal("hermex-admin: admin_secret is required to serve the admin API")
		}
		addr := cfg.AdminAddr
		if addr == "" {
			addr = ":8081"
		}
		ldapVerifier := ldapauth.New()
		dir.SetLDAPVerifier(ldapVerifier) // an administrator may be LDAP-mastered
		logger, logClose := logging.Build("hermex-admin", cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
		srv := admin.NewServer(dir, cfg, []byte(cfg.AdminSecret))
		// Failed-login lockout: read the stored tuning at startup and re-read it every
		// minute, so an operator can tighten it during a credential-stuffing wave, or
		// loosen it when legitimate users are being locked out, without a restart.
		authlimit.Apply("hermex-admin", logger, srv.Limiter(), dir.GetLoginLockoutSettings)
		go authlimit.RunMaintenance("hermex-admin", logger, srv.Limiter(), dir.GetLoginLockoutSettings)
		srv.SetLogger(logger)           // a failing request records its real error here, not in the response
		srv.SetLDAPSyncer(ldapVerifier) // enables the Directory Sync trigger
		// The panel reports the quarantine digest as unable to send without this,
		// rather than rendering the stored toggle as if it were the whole story.
		srv.SetDigestSigning(cfg.DigestSecret != "")
		var targets []admin.HealthTarget
		for _, t := range cfg.HealthTargets {
			targets = append(targets, admin.HealthTarget{Name: t.Name, URL: t.URL})
		}
		srv.SetHealthTargets(targets) // enables the Live status monitor
		cleanups := []func() error{logClose}
		var logReader *logging.Reader
		if cfg.MongoURI != "" {
			logReader, err = logging.NewReader(cfg.MongoURI, cfg.LogDatabase)
			if err != nil {
				log.Fatalf("hermex-admin: log reader: %v", err)
			}
			srv.SetLogReader(logReader) // enables the web UI log viewer
			cleanups = append(cleanups, logReader.Close)
		}
		// Per-client HTTP request limiter: read the stored settings at startup and
		// re-read them every minute, so an operator's change applies without a
		// restart. It is off until an operator enables it, and any read failure
		// leaves it as it is, so a settings problem never locks an operator out of
		// the panel that turns it off.
		httpLimiter := httplimit.NewLimiter()
		httplimit.Apply("hermex-admin", logger, httpLimiter, dir.GetHTTPRateLimitSettings)
		go httplimit.RunMaintenance("hermex-admin", logger, httpLimiter, dir.GetHTTPRateLimitSettings)
		// TLS certificates come from the provider: the config-file cert as a
		// fallback, overridden by an admin-uploaded cert the provider polls for, so
		// a renewal applies without a restart. The panel where certificates are
		// uploaded must not be the one place that needs a restart to serve them.
		provider, err := tlscert.New(cfg, dir, logger)
		if err != nil {
			log.Fatalf("hermex-admin: tls: %v", err)
		}
		if provider.TLSEnabled() {
			go provider.RunMaintenance()
		}
		hs, err := serve.New(addr, srv.Handler(), provider, logger, logging.Admin, httpLimiter)
		if err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
		logger.Info(logging.Admin, "daemon.startup", logging.Fields{"daemon": "admin", "addr": addr})
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go srv.RunTaskWorker(ctx, 5*time.Second) // drains the async admin task queue
		if logReader != nil {
			// Enforce the operator's log-retention window by pruning the store.
			go runLogRetention(ctx, dir, logReader, cfg.LogRetentionDays)
		}
		// Enforce the operator's Recoverable Items retention window across mailboxes.
		go runRecoverableRetention(ctx, dir)
		log.Printf("hermex-admin serving the admin API on %s", addr)
		// The same liveness and readiness contract every other daemon serves. This
		// one is the consumer of all of theirs for the Live monitor, and had none of
		// its own: nothing outside it could tell a running panel from one whose
		// directory connection is failing, since that surfaced only as individual
		// request errors.
		//
		// The log store is deliberately not probed. It self-heals by spilling to disk
		// and reconnecting, and no daemon depends on it at startup, so reporting the
		// panel degraded when it is down would contradict that.
		comps := append([]lifecycle.Component{hs},
			health.Components(cfg.HealthAddr, "admin", adminHealthChecks(db, provider)...)...)
		if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, comps, cleanups...); err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
	default:
		usage()
	}
}

// pinger is the directory-connection probe the health endpoint reports. It is an
// interface so a test can supply a connection that fails.
type pinger interface {
	PingContext(ctx context.Context) error
}

// adminHealthChecks builds the probes the admin daemon's health endpoint reports:
// the directory connection every request depends on, and the serving certificate's
// remaining validity when TLS is on, so a renewal that failed shows as degraded
// before clients start failing handshakes. It matches what every other daemon
// reports.
func adminHealthChecks(db pinger, provider *tlscert.Provider) []health.Check {
	checks := []health.Check{{Name: "directory", Probe: db.PingContext}}
	if provider != nil && provider.TLSEnabled() {
		checks = append(checks, tlscert.ExpiryCheck(provider))
	}
	return checks
}

// runLogRetention enforces the operator's central-log retention window by pruning the
// log store. Every minute it reads the configured window (in days) from the directory
// and deletes events older than that; a window of zero or less means keep forever, so
// nothing is pruned and the "delete everything" state is impossible. The directory row
// is seeded once from seedDays (the config value) so an existing deployment keeps its
// behaviour, after which the admin panel is the source of truth, applied without a
// restart. Earlier builds expired logs with a Mongo TTL index; that index is dropped
// once here so its stale window cannot override the operator's setting. It returns when
// ctx is cancelled.
func runLogRetention(ctx context.Context, dir *directory.SQLDirectory, reader *logging.Reader, seedDays int) {
	dropCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := reader.DropLegacyTTLIndex(dropCtx); err != nil {
		log.Printf("hermex-admin: drop legacy log TTL index: %v", err)
	}
	cancel()

	if _, found, err := dir.GetLogRetentionDays(); err == nil && !found {
		if err := dir.SetLogRetentionDays(seedDays); err != nil {
			log.Printf("hermex-admin: seed log retention: %v", err)
		}
	}

	prune := func() {
		days, _, err := dir.GetLogRetentionDays()
		if err != nil {
			log.Printf("hermex-admin: read log retention: %v", err)
			return
		}
		if days <= 0 {
			return // keep forever, never prune
		}
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		pruneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if n, err := reader.PruneOlderThan(pruneCtx, cutoff); err != nil {
			log.Printf("hermex-admin: prune logs: %v", err)
		} else if n > 0 {
			log.Printf("hermex-admin: pruned %d log events older than %d days", n, days)
		}
	}

	prune() // apply immediately at startup
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
}

// runRecoverableRetention enforces the operator's Recoverable Items retention window
// by sweeping every mailbox's dumpster every minute, permanently purging soft-deleted
// items older than the window (default 14 days; 0 or less disables auto-purge). The
// window is read each run, so an admin-panel change applies without a restart. It
// returns when ctx is cancelled.
func runRecoverableRetention(ctx context.Context, dir *directory.SQLDirectory) {
	sweep := func() {
		if n, err := dir.SweepRecoverableItems(time.Now()); err != nil {
			log.Printf("hermex-admin: sweep recoverable items: %v", err)
		} else if n > 0 {
			log.Printf("hermex-admin: purged %d expired recoverable items", n)
		}
	}
	sweep() // apply immediately at startup
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}

// defaultEMLPruneDays is the age below which a cached wire copy is kept. Recent
// mail is what clients actually fetch, so sparing a month keeps the working set
// warm and still reclaims the long tail.
const defaultEMLPruneDays = 30

// pruneEML drops cached wire copies older than days from one mailbox, or from
// every mailbox when target is "all".
//
// The cache holds a second copy of every live message and roughly doubles the
// space a mailbox occupies, and nothing evicts it on its own: it is dropped when
// a message is deleted or moved, never while the message is still there. This is
// the operator's lever for reclaiming that space without deleting mail. Removing
// a cached copy is always safe because the store re-synthesizes it from the
// stored object on the next read, so the only cost is one re-export per pruned
// message that is read again.
func pruneEML(dir *directory.SQLDirectory, target string, days int) {
	var maildirs []string
	if target == "all" {
		var err error
		if maildirs, err = dir.AllMaildirs(); err != nil {
			log.Fatalf("hermex-admin: list mailboxes: %v", err)
		}
	} else {
		maildir, ok := dir.Resolve(target)
		if !ok {
			log.Fatalf("hermex-admin: unknown or unreceivable mailbox: %s", target)
		}
		maildirs = []string{maildir}
	}

	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	var files, boxes int
	var reclaimed int64
	for _, md := range maildirs {
		store, err := objectstore.Open(md)
		if err != nil {
			// One unreadable mailbox must not abandon the rest of the run; report
			// it and carry on, so a single bad account cannot block reclamation.
			log.Printf("hermex-admin: open mailbox %s: %v", md, err)
			continue
		}
		n, bytes, err := store.PruneEMLCache(cutoff)
		store.Close()
		if err != nil {
			log.Printf("hermex-admin: prune %s: %v", md, err)
		}
		files += n
		reclaimed += bytes
		if n > 0 {
			boxes++
		}
	}
	fmt.Printf("pruned %d cached wire copies (%d bytes) from %d mailbox(es)\n", files, reclaimed, boxes)
}

// backupMail writes a consistent copy of one mailbox, or of every mailbox and
// every domain's public store, under dest. The directory dump covers the accounts
// and the key material; this covers the mail itself, which nothing else in the
// tooling did.
//
// The copy mirrors the source layout (user/<domain>/<local>, domain/<domain>), so
// restoring is copying a directory back into place with the services stopped, not
// running an importer. It works on a live mailbox: the databases are snapshotted
// inside a read transaction rather than copied as files.
func backupMail(dir *directory.SQLDirectory, cfg *config.Config, target, dest string) {
	// sources pairs each store directory with its path relative to the data root,
	// which is where it is written under dest.
	type source struct{ dir, rel string }
	var sources []source

	if target == "all" {
		maildirs, err := dir.AllMaildirs()
		if err != nil {
			log.Fatalf("hermex-admin: list mailboxes: %v", err)
		}
		for _, md := range maildirs {
			sources = append(sources, source{md, backupRel(cfg, md)})
		}
		domains, err := dir.ListDomains()
		if err != nil {
			log.Fatalf("hermex-admin: list domains: %v", err)
		}
		for _, d := range domains {
			// A domain has a public store only once something is filed in it, so an
			// absent directory is normal rather than a fault.
			home := cfg.HomedirFor(d.Name)
			if _, err := os.Stat(filepath.Join(home, "objects.sqlite3")); err != nil {
				continue
			}
			sources = append(sources, source{home, backupRel(cfg, home)})
		}
	} else {
		maildir, ok := dir.Resolve(target)
		if !ok {
			log.Fatalf("hermex-admin: unknown or unreceivable mailbox: %s", target)
		}
		sources = append(sources, source{maildir, backupRel(cfg, maildir)})
	}

	var done int
	for _, src := range sources {
		store, err := objectstore.Open(src.dir)
		if err != nil {
			// One unreadable mailbox must not abandon the rest of the run: a backup
			// that covers all but one account is worth far more than none.
			log.Printf("hermex-admin: open mailbox %s: %v", src.dir, err)
			continue
		}
		err = store.Backup(filepath.Join(dest, src.rel))
		store.Close()
		if err != nil {
			log.Printf("hermex-admin: back up %s: %v", src.dir, err)
			continue
		}
		done++
	}
	fmt.Printf("backed up %d of %d mailbox(es) to %s\n", done, len(sources), dest)
	if done < len(sources) {
		// The exit status is what a scheduled run checks, so a partial backup must
		// not look like a clean one.
		os.Exit(1)
	}
}

// backupRel maps a store directory to its path under the backup root. A store
// inside the data root keeps its own relative path; one on a separate placement
// partition keeps the tail that identifies it (user/<domain>/<local>), so two
// partitions never collide and the layout stays restorable by hand.
func backupRel(cfg *config.Config, storeDir string) string {
	if rel, err := filepath.Rel(cfg.DataDir, storeDir); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	parts := strings.Split(filepath.ToSlash(storeDir), "/")
	if n := len(parts); n >= 3 {
		return filepath.Join(parts[n-3], parts[n-2], parts[n-1])
	}
	return filepath.Base(storeDir)
}

// exportDKIM writes a domain's DKIM signing key to stdout as PEM, the operator's
// path for keeping a copy of key material the panel only ever generates inward.
//
// It writes nothing but the key to stdout, so the output can be redirected
// straight into a file; the selector and the reminder go to stderr. A key waiting
// on its DNS record is exported too: that is the state whose loss goes unnoticed
// the longest.
func exportDKIM(dir *directory.SQLDirectory, domain string) {
	privPEM, selector, found, err := dir.ExportDKIMKey(domain)
	if err != nil {
		log.Fatalf("hermex-admin: export dkim: %v", err)
	}
	if !found {
		log.Fatalf("hermex-admin: %s has no DKIM key", domain)
	}
	fmt.Fprintf(os.Stderr, "selector %s._domainkey.%s\n", selector, domain)
	fmt.Fprintln(os.Stderr, "this is a private key: store it where the database backup is stored")
	os.Stdout.Write(privPEM)
}

// listSessions prints an account's live sessions on both signed-in surfaces, so an
// operator can see what is connected before deciding to end it.
func listSessions(dir *directory.SQLDirectory, email string) {
	now := time.Now().Unix()
	web, err := dir.ListWebmailSessions(email, now)
	if err != nil {
		log.Fatalf("hermex-admin: list webmail sessions: %v", err)
	}
	panel, err := dir.ListAdminSessions(email, now)
	if err != nil {
		log.Fatalf("hermex-admin: list panel sessions: %v", err)
	}
	if len(web) == 0 && len(panel) == 0 {
		fmt.Printf("%s has no live sessions\n", email)
		return
	}
	for _, s := range web {
		fmt.Printf("webmail  %s  from %s  last active %s  expires %s\n",
			s.DeviceType, s.ClientIP,
			time.Unix(s.LastActive, 0).Format(time.RFC3339),
			time.Unix(s.ExpiresAt, 0).Format(time.RFC3339))
	}
	for _, s := range panel {
		fmt.Printf("panel    signed in %s  expires %s\n",
			time.Unix(s.CreatedAt, 0).Format(time.RFC3339),
			time.Unix(s.ExpiresAt, 0).Format(time.RFC3339))
	}
}

// revokeSessions ends every session an account holds on both surfaces. It is the
// compromise-response lever: a stolen cookie is valid for the rest of its life
// however many times its password is changed elsewhere, and until now the only
// way to end one was to restart every daemon. Both surfaces are revoked together
// because an operator responding to a leak does not know which one leaked.
func revokeSessions(dir *directory.SQLDirectory, email string) {
	web, err := dir.DeleteWebmailSessionsFor(email)
	if err != nil {
		log.Fatalf("hermex-admin: revoke webmail sessions: %v", err)
	}
	panel, err := dir.CountedDeleteAdminSessionsFor(email)
	if err != nil {
		log.Fatalf("hermex-admin: revoke panel sessions: %v", err)
	}
	fmt.Printf("revoked %d webmail and %d panel session(s) for %s\n", web, panel, email)
}
