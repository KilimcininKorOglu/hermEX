package activesync

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/wbxml"
)

// TestResolveRecipientsRecordsAnUnreadableAddressBook proves a contact search that
// cannot read the caller's mailbox is recorded, while the answer still comes from
// the directory: an unreadable store must not look like an empty address book to
// the operator. The caller's mailbox here does not exist, so it cannot be opened.
//
// A directory entry whose own mailbox was never provisioned simply has no
// picture, which is not a failure and records nothing.
func TestResolveRecipientsRecordsAnUnreadableAddressBook(t *testing.T) {
	root := t.TempDir()
	accs := directory.StaticAccounts{
		testUser:            {Password: testPass, MailboxPath: filepath.Join(root, "missing")},
		"ghost@hermex.test": {Password: "x", MailboxPath: filepath.Join(root, "never-provisioned")},
	}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sink := &policySink{}
	srv.Logger = logging.New(sink)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	_, resp := postCommand(t, ts, "ResolveRecipients", pictureRequest("ghost"))
	r := responseFor(resp, "ghost")
	if r == nil || r.Child(wbxml.RRRecipient) == nil {
		t.Fatal("the directory entry did not resolve")
	}

	var contacts, photos int
	for _, e := range sink.events {
		switch e.Name {
		case "resolve.contacts.fail":
			contacts++
			if e.User != testUser {
				t.Errorf("resolve.contacts.fail user = %q, want %q", e.User, testUser)
			}
		case "resolve.photo.fail":
			photos++
		}
	}
	if contacts == 0 {
		t.Error("an unreadable address book recorded no resolve.contacts.fail event")
	}
	if photos != 0 {
		t.Errorf("a never-provisioned mailbox recorded %d resolve.photo.fail events, want 0", photos)
	}
}
