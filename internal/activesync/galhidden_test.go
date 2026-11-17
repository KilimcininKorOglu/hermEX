package activesync

import (
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/wbxml"
)

// hidingAccounts is a directory whose GAL carries the operator's hide mask, which
// the static directory never sets.
type hidingAccounts struct {
	directory.StaticAccounts
	hidden map[string]uint32
}

func (h hidingAccounts) SearchGAL(caller, q string, limit int) ([]directory.GALEntry, error) {
	entries, err := h.StaticAccounts.SearchGAL(caller, q, limit)
	for i := range entries {
		entries[i].HiddenFrom = h.hidden[entries[i].Address]
	}
	return entries, err
}

// hidingServer starts a server whose GAL holds one visible and one hidden user,
// both matching the query prefix "al".
func hidingServer(t *testing.T, mask uint32) *httptest.Server {
	t.Helper()
	accs := hidingAccounts{
		StaticAccounts: directory.StaticAccounts{
			testUser:             {Password: testPass, MailboxPath: filepath.Join(t.TempDir(), "alice")},
			"albert@hermex.test": {Password: testPass, MailboxPath: filepath.Join(t.TempDir(), "albert")},
		},
		hidden: map[string]uint32{"albert@hermex.test": mask},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

// galAddresses collects every address a Search reply carried.
func galAddresses(store *wbxml.Node) []string {
	var out []string
	for _, result := range store.Children {
		if result.Tag != wbxml.SRResult {
			continue
		}
		if props := result.Child(wbxml.SRProperties); props != nil {
			out = append(out, props.ChildText(wbxml.GALEmailAddress))
		}
	}
	return out
}

// TestSearchGALOmitsHiddenUser proves a GAL Search does not return a user the
// operator hid from the address book, and still returns the one it did not.
func TestSearchGALOmitsHiddenUser(t *testing.T) {
	ts := hidingServer(t, directory.HideFromGAL)

	_, root := postCommand(t, ts, "Search", searchReq("GAL", "al"))
	store := searchStore(t, root)
	got := galAddresses(store)
	for _, addr := range got {
		if addr == "albert@hermex.test" {
			t.Error("a GAL Search returned a user hidden from the address book")
		}
	}
	if len(got) != 1 || got[0] != testUser {
		t.Errorf("Search results = %v, want only the visible user", got)
	}
	if total := store.ChildText(wbxml.SRTotal); total != "1" {
		t.Errorf("Total = %q, want 1: the hidden user must not be counted", total)
	}
}

// TestHiddenUserStaysHiddenByExactAddress proves a caller who already knows a
// hidden user's full address cannot read the entry back by asking for it: the
// hide switch withholds the user from the surface, not only from a prefix list.
func TestHiddenUserStaysHiddenByExactAddress(t *testing.T) {
	const hidden = "albert@hermex.test"
	ts := hidingServer(t, directory.HideFromGAL)
	_, root := postCommand(t, ts, "Search", searchReq("GAL", hidden))
	if got := galAddresses(searchStore(t, root)); len(got) != 0 {
		t.Errorf("a GAL Search for the hidden address returned %v", got)
	}

	ts = hidingServer(t, directory.HideFromResolve)
	_, root = postCommand(t, ts, "ResolveRecipients",
		wbxml.Elem(wbxml.RRResolveRecipients, wbxml.Str(wbxml.RRTo, hidden)))
	if resp := root.Child(wbxml.RRResponse); resp != nil {
		for _, rec := range resp.Children {
			if rec.Tag == wbxml.RRRecipient && rec.ChildText(wbxml.RREmailAddress) == hidden {
				t.Error("ResolveRecipients returned the hidden user asked for by address")
			}
		}
	}
}

// failingGAL is a directory whose address book cannot be read.
type failingGAL struct{ directory.StaticAccounts }

func (failingGAL) SearchGAL(string, string, int) ([]directory.GALEntry, error) {
	return nil, errors.New("directory unreachable")
}

// TestSearchGALReportsADirectoryFailure proves a GAL Search the directory cannot
// answer reports Store Status 3, a server error ([MS-ASCMD] 2.2.3.177.15), and
// never a successful empty result the device would show as "no such person".
func TestSearchGALReportsADirectoryFailure(t *testing.T) {
	accs := failingGAL{directory.StaticAccounts{
		testUser: {Password: testPass, MailboxPath: filepath.Join(t.TempDir(), "alice")},
	}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	_, root := postCommand(t, ts, "Search", searchReq("GAL", "al"))
	wantEq(t, "Store Status", searchStore(t, root).ChildText(wbxml.SRStatus), "3")

	// ResolveRecipients: Status 6, a server error ([MS-ASCMD] 2.2.3.177.14), not
	// an unresolved recipient the device would offer to send to anyway.
	_, root = postCommand(t, ts, "ResolveRecipients",
		wbxml.Elem(wbxml.RRResolveRecipients, wbxml.Str(wbxml.RRTo, "al")))
	wantEq(t, "ResolveRecipients Status", root.ChildText(wbxml.RRStatus), "6")
}

// TestSearchGALKeepsResolutionHiddenUser proves the browse surface follows the
// address book: a user withheld only from name resolution still appears in a GAL
// search, so the two switches stay distinct.
func TestSearchGALKeepsResolutionHiddenUser(t *testing.T) {
	ts := hidingServer(t, directory.HideFromResolve)

	_, root := postCommand(t, ts, "Search", searchReq("GAL", "al"))
	if got := galAddresses(searchStore(t, root)); len(got) != 2 {
		t.Errorf("Search results = %v, want both users", got)
	}
}

// TestResolveRecipientsOmitsHiddenUser proves name resolution withholds a user
// hidden from resolution, the switch that exists to keep an address out of
// auto-completion.
func TestResolveRecipientsOmitsHiddenUser(t *testing.T) {
	ts := hidingServer(t, directory.HideFromResolve)

	_, root := postCommand(t, ts, "ResolveRecipients",
		wbxml.Elem(wbxml.RRResolveRecipients, wbxml.Str(wbxml.RRTo, "al")))
	resp := root.Child(wbxml.RRResponse)
	if resp == nil {
		t.Fatal("ResolveRecipients carried no Response")
	}
	var got []string
	for _, rec := range resp.Children {
		if rec.Tag == wbxml.RRRecipient {
			got = append(got, rec.ChildText(wbxml.RREmailAddress))
		}
	}
	for _, addr := range got {
		if addr == "albert@hermex.test" {
			t.Error("ResolveRecipients returned a user hidden from name resolution")
		}
	}
	if len(got) != 1 || got[0] != testUser {
		t.Errorf("resolved recipients = %v, want only the visible user", got)
	}
}
