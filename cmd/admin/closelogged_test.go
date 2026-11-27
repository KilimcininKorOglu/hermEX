package main

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

type failingCloser struct{ err error }

func (f failingCloser) Close() error { return f.err }

// TestCloseLoggedPrintsAFailedClose proves a store or spool that fails to close
// leaves a line naming it and the error, so a maintenance run whose last write
// may not have reached disk does not read as a clean run.
func TestCloseLoggedPrintsAFailedClose(t *testing.T) {
	var out bytes.Buffer
	prev, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&out)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(prevFlags) })

	closeLogged("mailbox /data/alice", failingCloser{errors.New("database is locked")})
	if got := out.String(); !strings.Contains(got, "mailbox /data/alice") || !strings.Contains(got, "database is locked") {
		t.Errorf("output = %q, want the closed object and the error", got)
	}

	out.Reset()
	closeLogged("mailbox /data/bob", failingCloser{})
	if out.Len() != 0 {
		t.Errorf("a clean close printed %q, want nothing", out.String())
	}
}
