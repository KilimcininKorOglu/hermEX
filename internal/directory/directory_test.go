package directory

import "testing"

// TestStaticAccountsMaildirs checks that MailboxLister over the static account
// map returns each mailbox once: several addresses that share a mailbox collapse
// to a single path, and an account with no mailbox is skipped. The send-later
// spooler relies on this distinct, non-empty set to scan each store exactly once.
func TestStaticAccountsMaildirs(t *testing.T) {
	a := StaticAccounts{
		"alice@hermex.test":  {Password: "x", MailboxPath: "/m/alice"},
		"alias@hermex.test":  {Password: "x", MailboxPath: "/m/alice"}, // same mailbox as alice
		"bob@hermex.test":    {Password: "x", MailboxPath: "/m/bob"},
		"nopath@hermex.test": {Password: "x", MailboxPath: ""}, // no mailbox: skipped
	}
	got, err := a.Maildirs()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"/m/alice": true, "/m/bob": true}
	if len(got) != len(want) {
		t.Fatalf("Maildirs = %v, want the %d distinct non-empty paths", got, len(want))
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected maildir %q", p)
		}
	}
}
