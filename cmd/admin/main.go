// Command admin provisions the hermEX directory: it ensures the schema and
// creates domains, users, and aliases in the directory database, and serves the
// admin API.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/admin"
	"hermex/internal/config"
	"hermex/internal/directory"
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
	fmt.Fprintln(os.Stderr, "  sweep-content <email>   (reclaim orphan content files; run with the mailbox idle)")
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
		if err != nil {
			log.Fatalf("hermex-admin: sweep: %v", err)
		}
		fmt.Printf("swept %d orphan content file(s) from %s\n", removed, args[1])
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
		logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir)
		srv := admin.NewServer(dir, cfg, []byte(cfg.AdminSecret))
		srv.SetLogger(logger)           // a failing request records its real error here, not in the response
		srv.SetLDAPSyncer(ldapVerifier) // enables the Directory Sync trigger
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
		httplimit.Apply("hermex-admin", httpLimiter, dir.GetHTTPRateLimitSettings)
		go httplimit.RunMaintenance("hermex-admin", httpLimiter, dir.GetHTTPRateLimitSettings)
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
		if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, []lifecycle.Component{hs}, cleanups...); err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
	default:
		usage()
	}
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
			return // keep forever — never prune
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
