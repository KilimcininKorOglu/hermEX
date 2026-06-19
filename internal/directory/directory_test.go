package directory

import (
	"sort"
	"testing"
)

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

// TestStaticAccountsIsLocalDomain checks the LocalDomains predicate over the
// static account map: a domain that hosts an account is local, an outside domain
// is not, and the match is case-insensitive. Relay routing depends on this to
// avoid relaying mail back at a domain this server serves.
func TestStaticAccountsIsLocalDomain(t *testing.T) {
	a := StaticAccounts{
		"alice@hermex.test": {Password: "x", MailboxPath: "/m/alice"},
		"bob@hermex.test":   {Password: "x", MailboxPath: "/m/bob"},
	}
	for _, tc := range []struct {
		domain string
		want   bool
	}{
		{"hermex.test", true},
		{"Hermex.Test", true}, // case-insensitive
		{"gmail.com", false},
		{"", false},
	} {
		got, err := a.IsLocalDomain(tc.domain)
		if err != nil {
			t.Fatalf("IsLocalDomain(%q): %v", tc.domain, err)
		}
		if got != tc.want {
			t.Errorf("IsLocalDomain(%q) = %v, want %v", tc.domain, got, tc.want)
		}
	}
}

// TestStaticAccountsSearchGAL checks the GAL substring search over the static
// account map: a case-insensitive address match, collapsing addresses that share
// a mailbox to one suggestion, skipping accounts with no mailbox, address
// ordering, the result cap, and the address mirrored into the display name.
func TestStaticAccountsSearchGAL(t *testing.T) {
	a := StaticAccounts{
		"alice@hermex.test":  {Password: "x", MailboxPath: "/m/alice"},
		"alias@hermex.test":  {Password: "x", MailboxPath: "/m/alice"}, // same mailbox as alice
		"bob@hermex.test":    {Password: "x", MailboxPath: "/m/bob"},
		"carol@hermex.test":  {Password: "x", MailboxPath: "/m/carol"},
		"nopath@hermex.test": {Password: "x", MailboxPath: ""}, // no mailbox: never suggested
	}

	// "ali" matches both alice and alias, which share a mailbox, so the suggestion
	// collapses to a single entry.
	got, err := a.SearchGAL("ali", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("SearchGAL(%q) = %v, want one entry (the shared mailbox collapses)", "ali", got)
	}
	if got[0].DisplayName != got[0].Address {
		t.Errorf("DisplayName %q should mirror Address %q", got[0].DisplayName, got[0].Address)
	}

	// Matching is case-insensitive.
	if got, _ := a.SearchGAL("BOB", 0); len(got) != 1 || got[0].Address != "bob@hermex.test" {
		t.Errorf("SearchGAL(%q) = %v, want [bob@hermex.test]", "BOB", got)
	}

	// A domain-wide substring returns one entry per distinct mailbox, ordered by
	// address; the empty-mailbox account is excluded.
	all, _ := a.SearchGAL("@hermex.test", 0)
	if len(all) != 3 {
		t.Fatalf("SearchGAL(domain) = %v, want 3 distinct mailboxes", all)
	}
	if !sort.SliceIsSorted(all, func(i, j int) bool { return all[i].Address < all[j].Address }) {
		t.Errorf("SearchGAL results not ordered by address: %v", all)
	}

	// The limit caps the result count.
	if got, _ := a.SearchGAL("@hermex.test", 2); len(got) != 2 {
		t.Errorf("SearchGAL(domain, limit 2) returned %d entries, want 2", len(got))
	}
}
