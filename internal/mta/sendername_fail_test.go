package mta

import (
	"bytes"
	"errors"
	"testing"

	"hermex/internal/directory"
)

// failingNames is a directory whose outgoing display-name lookup fails.
type failingNames struct {
	directory.StaticAccounts
}

func (failingNames) OutgoingDisplayNames(string) (string, string, error) {
	return "", "", errors.New("db down")
}

// TestOutgoingCopiesRecordsAFailedNameLookup proves a display-name lookup that
// fails still sends the message unchanged, and leaves a trace: otherwise every
// mail of a tenant that set a display name goes out without it and nothing tells
// the operator why.
func TestOutgoingCopiesRecordsAFailedNameLookup(t *testing.T) {
	sink := withPassLogger(t)
	raw := []byte("From: Alice <alice@hermex.test>\r\nSubject: hi\r\n\r\nbody\r\n")

	local, relayed := outgoingCopies(failingNames{}, "alice@hermex.test", raw)

	if !bytes.Equal(local, raw) || !bytes.Equal(relayed, raw) {
		t.Error("a failed lookup must leave both copies unchanged")
	}
	e, ok := sink.find("delivery.pass.fail")
	if !ok {
		t.Fatal("the failed display-name lookup was not recorded")
	}
	if e.Fields["pass"] != "outgoing-display-name" || e.User != "alice@hermex.test" {
		t.Errorf("event = pass %v user %q, want the display-name pass for the sender", e.Fields["pass"], e.User)
	}
}
