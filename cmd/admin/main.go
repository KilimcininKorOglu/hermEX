// Command admin provisions the hermEX directory: it ensures the schema and
// creates domains, users, and aliases in the directory database.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/ldapauth"
	"hermex/internal/objectstore"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hermex-admin -config <file> <command> [args]")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  ensure-schema")
	fmt.Fprintln(os.Stderr, "  create-domain <domainname>")
	fmt.Fprintln(os.Stderr, "  create-user <email> <password>")
	fmt.Fprintln(os.Stderr, "  create-alias <alias-address> <user-email>")
	fmt.Fprintln(os.Stderr, "  sweep-content <email>   (reclaim orphan content files; run with the mailbox idle)")
	fmt.Fprintln(os.Stderr, "  ldap-sync <org-id>      (import the org's LDAP/AD accounts into the directory)")
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
	default:
		usage()
	}
}
