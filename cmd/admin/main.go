// Command admin provisions the hermEX directory: it ensures the schema and
// creates domains, users, and aliases in the directory database.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"

	_ "github.com/go-sql-driver/mysql"

	"hermex/internal/config"
	"hermex/internal/directory"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hermex-admin -config <file> <command> [args]")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  ensure-schema")
	fmt.Fprintln(os.Stderr, "  create-domain <domainname>")
	fmt.Fprintln(os.Stderr, "  create-user <email> <password>")
	fmt.Fprintln(os.Stderr, "  create-alias <alias-address> <user-email>")
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
	default:
		usage()
	}
}
