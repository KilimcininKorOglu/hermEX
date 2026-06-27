package activesync

import (
	"testing"

	"hermex/internal/wbxml"
)

// findReq builds a Find request with a MailboxSearchCriterion over the given
// freetext, optionally carrying an Options>Range.
func findReq(freetext, rng string) *wbxml.Node {
	crit := []*wbxml.Node{
		wbxml.Elem(wbxml.FNDQuery, wbxml.Str(wbxml.FNDFreeText, freetext)),
	}
	if rng != "" {
		crit = append(crit, wbxml.Elem(wbxml.FNDOptions, wbxml.Str(wbxml.FNDRange, rng)))
	}
	return wbxml.Elem(wbxml.FNDFind,
		wbxml.Elem(wbxml.FNDExecuteSearch,
			wbxml.Elem(wbxml.FNDMailboxSearchCriterion, crit...)))
}

// findResponse returns the Response node of a Find reply.
func findResponse(t *testing.T, root *wbxml.Node) *wbxml.Node {
	t.Helper()
	resp := root.Child(wbxml.FNDResponse)
	if resp == nil {
		t.Fatal("Find reply carried no Response")
	}
	return resp
}

// TestFindMailbox proves a Find MailboxSearchCriterion matches a message and
// returns a Result carrying the email's Class, ServerId, CollectionId, and the
// Properties preview, under a Response naming the Mailbox store.
func TestFindMailbox(t *testing.T) {
	ts, dir := seededServer(t)
	seedSearchMessage(t, dir, "Quarterly revenue report", "the body text")

	_, root := postCommand(t, ts, "Find", findReq("quarterly", ""))
	if s := root.ChildText(wbxml.FNDStatus); s != "1" {
		t.Fatalf("overall Status = %q, want 1", s)
	}
	resp := findResponse(t, root)
	if store := resp.ChildText(wbxml.IOStore); store != "Mailbox" {
		t.Errorf("Response Store = %q, want Mailbox", store)
	}
	if total := resp.ChildText(wbxml.FNDTotal); total != "1" {
		t.Errorf("Total = %q, want 1", total)
	}
	result := resp.Child(wbxml.FNDResult)
	if result == nil {
		t.Fatal("Find carried no Result")
	}
	if c := result.ChildText(wbxml.ASClass); c != "Email" {
		t.Errorf("Result Class = %q, want Email", c)
	}
	if result.ChildText(wbxml.ASServerID) == "" {
		t.Error("Result carried no ServerId")
	}
	if cid := result.ChildText(wbxml.ASCollectionID); cid != inboxID() {
		t.Errorf("Result CollectionId = %q, want %q", cid, inboxID())
	}
	props := result.Child(wbxml.FNDProperties)
	if props == nil {
		t.Fatal("Result carried no Properties")
	}
	if subj := props.ChildText(wbxml.EMSubject); subj != "Quarterly revenue report" {
		t.Errorf("Result Subject = %q, want the seeded subject", subj)
	}
}

// TestFindPaging proves the Options>Range slices the result set: three matches with
// Range "0-0" returns one Result but Total counts all three, and the returned Range
// reflects the page.
func TestFindPaging(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 3) // subjects "Message 1/2/3"

	_, root := postCommand(t, ts, "Find", findReq("message", "0-0"))
	resp := findResponse(t, root)
	if total := resp.ChildText(wbxml.FNDTotal); total != "3" {
		t.Errorf("Total = %q, want 3 (all matches counted)", total)
	}
	if rng := resp.ChildText(wbxml.FNDRange); rng != "0-0" {
		t.Errorf("Range = %q, want 0-0 (one item returned)", rng)
	}
	n := 0
	for _, c := range resp.Children {
		if c.Tag == wbxml.FNDResult {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Result count = %d, want 1 (Range 0-0)", n)
	}
}

// TestFindGalRejected proves a GalSearchCriterion is reported invalid-request: Find
// over the GAL is not implemented (matching the reference), so the device falls back
// to the Search command for address-book lookups.
func TestFindGalRejected(t *testing.T) {
	ts, _ := seededServer(t)

	req := wbxml.Elem(wbxml.FNDFind,
		wbxml.Elem(wbxml.FNDExecuteSearch,
			wbxml.Str(wbxml.FNDGalSearchCriterion, "alice")))
	_, root := postCommand(t, ts, "Find", req)
	if s := root.ChildText(wbxml.FNDStatus); s != "2" {
		t.Errorf("overall Status = %q, want 2 (invalid request: GAL-in-Find unsupported)", s)
	}
}
