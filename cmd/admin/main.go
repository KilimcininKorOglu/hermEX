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

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/admin"
	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/ldapauth"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/serve"
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
	fmt.Fprintln(os.Stderr, "  sweep-content <email>   (reclaim orphan content files; run with the mailbox idle)")
	fmt.Fprintln(os.Stderr, "  ldap-sync <org-id>      (import the org's LDAP/AD accounts into the directory)")
	fmt.Fprintln(os.Stderr, "  grant-admin <email> <system|org|domain> [scope-id]")
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
		users, err := ldapauth.New().Sync(lcfg)
		if err != nil {
			log.Fatalf("hermex-admin: ldap sync: %v", err)
		}
		var created, updated int
		for _, u := range users {
			// A directory entry whose mail domain is not provisioned locally is
			// skipped (logged) rather than aborting the whole sync.
			isNew, err := dir.UpsertLDAPUser(u.Username, u.ExternID, cfg.MaildirFor(u.Username))
			if err != nil {
				log.Printf("hermex-admin: skip %s: %v", u.Username, err)
				continue
			}
			if isNew {
				created++
			} else {
				updated++
			}
		}
		fmt.Printf("ldap-sync org %d: %d created, %d updated (of %d directory entries)\n",
			orgID, created, updated, len(users))
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
	case "serve":
		if cfg.AdminSecret == "" {
			log.Fatal("hermex-admin: admin_secret is required to serve the admin API")
		}
		addr := cfg.AdminAddr
		if addr == "" {
			addr = ":8081"
		}
		dir.SetLDAPVerifier(ldapauth.New()) // an administrator may be LDAP-mastered
		logger, logClose := logging.Build(cfg.MongoURI, cfg.LogDatabase, cfg.LogSpillDir, cfg.LogRetentionDays)
		srv := admin.NewServer(dir, cfg, []byte(cfg.AdminSecret))
		cleanups := []func() error{logClose}
		if cfg.MongoURI != "" {
			reader, err := logging.NewReader(cfg.MongoURI, cfg.LogDatabase)
			if err != nil {
				log.Fatalf("hermex-admin: log reader: %v", err)
			}
			srv.SetLogReader(reader) // enables the web UI log viewer
			cleanups = append(cleanups, reader.Close)
		}
		hs, err := serve.New(addr, srv.Handler(), cfg, logger, logging.Admin)
		if err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
		logger.Info(logging.Admin, "daemon.startup", logging.Fields{"daemon": "admin", "addr": addr})
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		log.Printf("hermex-admin serving the admin API on %s", addr)
		if err := lifecycle.Run(ctx, lifecycle.DefaultShutdownTimeout, []lifecycle.Component{hs}, cleanups...); err != nil {
			log.Fatalf("hermex-admin: %v", err)
		}
	default:
		usage()
	}
}
