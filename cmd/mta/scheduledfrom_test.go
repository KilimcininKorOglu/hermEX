package main

import (
	"errors"
	"path/filepath"
	"testing"

	"hermex/internal/directory"
)

// TestScheduledFromNamesOnlyTheOwner proves the send-later release authorizes every
// From a scheduled message carries. An Outbox item can be written by IMAP APPEND,
// so its bytes are the client's: a From list, or a second From field in either
// order (RFC 5322 3.6 allows one), can name an address beside the owner's, and the
// relay signs for the From domain. Each is refused; the owner's own From passes.
func TestScheduledFromNamesOnlyTheOwner(t *testing.T) {
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: filepath.Join(t.TempDir(), "alice")},
		"bob@hermex.test":   {MailboxPath: filepath.Join(t.TempDir(), "bob")},
	}
	const body = "To: carol@external.test\r\nSubject: later\r\n\r\nhi\r\n"
	for _, tc := range []struct {
		name, from string
		refuse     bool
	}{
		{"the owner", "From: alice@hermex.test\r\n", false},
		{"another user", "From: bob@hermex.test\r\n", true},
		{"a list naming another user", "From: alice@hermex.test, bob@hermex.test\r\n", true},
		{"a second field after the owner's", "From: alice@hermex.test\r\nFrom: bob@hermex.test\r\n", true},
		{"a second field before the owner's", "From: bob@hermex.test\r\nFrom: alice@hermex.test\r\n", true},
		{"no From", "", true},
	} {
		err := authorizeScheduledFrom(accounts, "alice@hermex.test", []byte(tc.from+body))
		if tc.refuse && !errors.Is(err, errScheduledFrom) {
			t.Errorf("%s: authorizeScheduledFrom = %v, want errScheduledFrom", tc.name, err)
		}
		if !tc.refuse && err != nil {
			t.Errorf("%s: authorizeScheduledFrom = %v, want nil", tc.name, err)
		}
	}
}
