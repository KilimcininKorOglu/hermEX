package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
)

// setPasswordRequest is one parsed set-password command line.
type setPasswordRequest struct {
	email       string
	password    string
	forceChange bool // write the must-change-password flag to this value
	endSessions bool // end every live session of the account
}

// parseSetPassword reads the command line after the command name. Both steps
// beside the password change default to on, so a plain invocation answers a
// compromise the way the admin panel's reset does.
func parseSetPassword(args []string) (setPasswordRequest, error) {
	req := setPasswordRequest{forceChange: true, endSessions: true}
	var positional []string
	for _, a := range args {
		switch {
		case a == "--no-force-change":
			req.forceChange = false
		case a == "--keep-sessions":
			req.endSessions = false
		case strings.HasPrefix(a, "--"):
			return setPasswordRequest{}, fmt.Errorf("set-password: unknown option %q", a)
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 2 {
		return setPasswordRequest{}, errors.New("set-password needs an address and a password")
	}
	req.email, req.password = positional[0], positional[1]
	return req, nil
}

// runSetPassword replaces one account's password, mirroring the admin panel's
// reset so the two ways of doing this cannot answer differently. The new
// password is temporary (the account must change it at the next login) and every
// live session of the account ends, because a reset is usually the answer to a
// leak and a stolen cookie outlives a password change. --no-force-change CLEARS
// the flag rather than leaving whatever an earlier reset wrote, and
// --keep-sessions leaves the sessions alone.
//
// The password is a command-line argument, like create-user's, so it is visible
// to anything that can read this host's process list.
func runSetPassword(c *cmdContext) {
	req, err := parseSetPassword(c.args[1:])
	if err != nil {
		log.Fatalf("hermex-admin: %v", err)
	}
	found, err := c.dir.SetPassword(req.email, req.password)
	if err != nil {
		log.Fatalf("hermex-admin: set password: %v", err)
	}
	if !found {
		log.Fatalf("hermex-admin: no such user: %s", req.email)
	}
	if _, err := c.dir.RequirePasswordChange(req.email, req.forceChange); err != nil {
		log.Fatalf("hermex-admin: record the must-change-password flag: %v", err)
	}
	fmt.Printf("password set for %s (must change at next login: %t)\n", req.email, req.forceChange)
	if req.endSessions {
		revokeSessions(c.dir, req.email)
	}
}
