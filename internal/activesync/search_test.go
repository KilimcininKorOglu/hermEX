package activesync

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// searchReq builds a Search request against the named store for the given query.
func searchReq(name, query string) *wbxml.Node {
	return wbxml.Elem(wbxml.SRSearch,
		wbxml.Elem(wbxml.SRStore,
			wbxml.Str(wbxml.SRName, name),
			wbxml.Str(wbxml.SRQuery, query)))
}

// searchStore returns the Store node of a Search reply.
func searchStore(t *testing.T, root *wbxml.Node) *wbxml.Node {
	t.Helper()
	resp := root.Child(wbxml.SRResponse)
	if resp == nil {
		t.Fatal("Search reply carried no Response")
	}
	store := resp.Child(wbxml.SRStore)
	if store == nil {
		t.Fatal("Search Response carried no Store")
	}
	return store
}

// TestSearchGAL proves a GAL Search resolves a matching user into a Result with
// its display name and address, and reports the result Range and Total.
func TestSearchGAL(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "Search", searchReq("GAL", "alice"))
	if s := root.ChildText(wbxml.SRStatus); s != "1" {
		t.Fatalf("overall Status = %q, want 1", s)
	}
	store := searchStore(t, root)
	if s := store.ChildText(wbxml.SRStatus); s != "1" {
		t.Errorf("store Status = %q, want 1", s)
	}
	if total := store.ChildText(wbxml.SRTotal); total != "1" {
		t.Errorf("Total = %q, want 1", total)
	}
	if rng := store.ChildText(wbxml.SRRange); rng != "0-0" {
		t.Errorf("Range = %q, want 0-0", rng)
	}
	result := store.Child(wbxml.SRResult)
	if result == nil {
		t.Fatal("GAL search carried no Result")
	}
	props := result.Child(wbxml.SRProperties)
	if props == nil {
		t.Fatal("Result carried no Properties")
	}
	if addr := props.ChildText(wbxml.GALEmailAddress); addr != testUser {
		t.Errorf("Result address = %q, want %q", addr, testUser)
	}
	if props.ChildText(wbxml.GALDisplayName) == "" {
		t.Error("Result carried no display name")
	}
	// Some clients refuse to render a GAL entry without these elements present.
	if props.Child(wbxml.GALFirstName) == nil || props.Child(wbxml.GALLastName) == nil {
		t.Error("Result must carry FirstName and LastName elements")
	}
}

// TestSearchGALMultiple proves a query matching two GAL users returns both
// Results with a correct Range ("0-1") and Total ("2") — the off-by-one a single
// match would hide.
func TestSearchGALMultiple(t *testing.T) {
	ts := galServer(t)

	_, root := postCommand(t, ts, "Search", searchReq("GAL", "al"))
	store := searchStore(t, root)
	if total := store.ChildText(wbxml.SRTotal); total != "2" {
		t.Errorf("Total = %q, want 2", total)
	}
	if rng := store.ChildText(wbxml.SRRange); rng != "0-1" {
		t.Errorf("Range = %q, want 0-1", rng)
	}
	n := 0
	for _, c := range store.Children {
		if c.Tag == wbxml.SRResult {
			n++
		}
	}
	if n != 2 {
		t.Errorf("Result count = %d, want 2", n)
	}
}

// TestSearchGALNoMatch proves a GAL Search that matches nothing reports a
// successful store with no Result and no Total.
func TestSearchGALNoMatch(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "Search", searchReq("GAL", "no-such-user"))
	store := searchStore(t, root)
	if s := store.ChildText(wbxml.SRStatus); s != "1" {
		t.Errorf("store Status = %q, want 1 (an empty search still succeeds)", s)
	}
	if store.Child(wbxml.SRResult) != nil {
		t.Error("an empty GAL search must carry no Result")
	}
	if store.ChildText(wbxml.SRTotal) != "" {
		t.Error("an empty GAL search must carry no Total")
	}
}

// TestSearchUnsupportedStore proves a DocumentLibrary search (hermEX has no
// document store) reports a request-invalid store status rather than a false empty
// success.
func TestSearchUnsupportedStore(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "Search", searchReq("DocumentLibrary", "anything"))
	if s := root.ChildText(wbxml.SRStatus); s != "1" {
		t.Fatalf("overall Status = %q, want 1", s)
	}
	store := searchStore(t, root)
	if s := store.ChildText(wbxml.SRStatus); s != "2" {
		t.Errorf("unsupported-store Status = %q, want 2 (request invalid)", s)
	}
}

// mailboxSearchReq builds a Store=Mailbox Search with the freetext nested in
// Query>And, the shape a device sends.
func mailboxSearchReq(freetext string) *wbxml.Node {
	return wbxml.Elem(wbxml.SRSearch,
		wbxml.Elem(wbxml.SRStore,
			wbxml.Str(wbxml.SRName, "Mailbox"),
			wbxml.Elem(wbxml.SRQuery,
				wbxml.Elem(wbxml.SRAnd,
					wbxml.Str(wbxml.SRFreeText, freetext)))))
}

// seedSearchMessage appends one message with the given subject and body to the
// mailbox's Inbox, for a Mailbox-search match.
func seedSearchMessage(t *testing.T, dir, subject, body string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := "From: sender@hermex.test\r\nTo: alice@hermex.test\r\nSubject: " + subject +
		"\r\nDate: Mon, 15 Jun 2026 09:00:00 +0000\r\nMessage-ID: <s1@hermex.test>\r\n\r\n" + body + "\r\n"
	when := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), when, 0); err != nil {
		t.Fatal(err)
	}
}

// TestSearchMailbox proves a Store=Mailbox search matches a message on its subject
// and returns a Result carrying the email's Class, a LongId, the CollectionId, and
// the listing Properties the device renders.
func TestSearchMailbox(t *testing.T) {
	ts, dir := seededServer(t)
	seedSearchMessage(t, dir, "Quarterly revenue report", "the body text")

	_, root := postCommand(t, ts, "Search", mailboxSearchReq("quarterly"))
	if s := root.ChildText(wbxml.SRStatus); s != "1" {
		t.Fatalf("overall Status = %q, want 1", s)
	}
	store := searchStore(t, root)
	if s := store.ChildText(wbxml.SRStatus); s != "1" {
		t.Errorf("store Status = %q, want 1", s)
	}
	if total := store.ChildText(wbxml.SRTotal); total != "1" {
		t.Errorf("Total = %q, want 1", total)
	}
	result := store.Child(wbxml.SRResult)
	if result == nil {
		t.Fatal("mailbox search carried no Result")
	}
	if c := result.ChildText(wbxml.ASClass); c != "Email" {
		t.Errorf("Result Class = %q, want Email", c)
	}
	if result.ChildText(wbxml.SRLongId) == "" {
		t.Error("Result carried no LongId")
	}
	if cid := result.ChildText(wbxml.ASCollectionID); cid != inboxID() {
		t.Errorf("Result CollectionId = %q, want %q", cid, inboxID())
	}
	props := result.Child(wbxml.SRProperties)
	if props == nil {
		t.Fatal("Result carried no Properties")
	}
	if subj := props.ChildText(wbxml.EMSubject); subj != "Quarterly revenue report" {
		t.Errorf("Result Subject = %q, want the seeded subject", subj)
	}
}

// TestSearchMailboxBody proves a Store=Mailbox search matches on the message body
// when the subject and sender do not, the second pass the webmail search also runs.
func TestSearchMailboxBody(t *testing.T) {
	ts, dir := seededServer(t)
	seedSearchMessage(t, dir, "Lunch plans", "let us discuss the pelican migration")

	_, root := postCommand(t, ts, "Search", mailboxSearchReq("pelican"))
	store := searchStore(t, root)
	if total := store.ChildText(wbxml.SRTotal); total != "1" {
		t.Errorf("Total = %q, want 1 (matched on body)", total)
	}
}

// TestSearchMailboxNoMatch proves a Store=Mailbox search that matches nothing
// reports a successful store with no Result and no Total.
func TestSearchMailboxNoMatch(t *testing.T) {
	ts, dir := seededServer(t)
	seedSearchMessage(t, dir, "Quarterly revenue report", "the body text")

	_, root := postCommand(t, ts, "Search", mailboxSearchReq("no-such-term-xyz"))
	store := searchStore(t, root)
	if s := store.ChildText(wbxml.SRStatus); s != "1" {
		t.Errorf("store Status = %q, want 1 (an empty search still succeeds)", s)
	}
	if store.Child(wbxml.SRResult) != nil {
		t.Error("an empty mailbox search must carry no Result")
	}
	if store.ChildText(wbxml.SRTotal) != "" {
		t.Error("an empty mailbox search must carry no Total")
	}
}
