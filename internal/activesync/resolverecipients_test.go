package activesync

import (
	"testing"

	"hermex/internal/wbxml"
)

// resolveReq builds a ResolveRecipients request for the given query strings.
func resolveReq(queries ...string) *wbxml.Node {
	tos := make([]*wbxml.Node, 0, len(queries))
	for _, q := range queries {
		tos = append(tos, wbxml.Str(wbxml.RRTo, q))
	}
	return wbxml.Elem(wbxml.RRResolveRecipients, tos...)
}

// responseFor returns the Response node whose echoed To matches the query.
func responseFor(root *wbxml.Node, query string) *wbxml.Node {
	for _, c := range root.Children {
		if c.Tag == wbxml.RRResponse && c.ChildText(wbxml.RRTo) == query {
			return c
		}
	}
	return nil
}

// TestResolveRecipientsResolved proves a query matching a GAL user resolves to a
// recipient carrying its display name and address.
func TestResolveRecipientsResolved(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "ResolveRecipients", resolveReq("alice"))
	if s := root.ChildText(wbxml.RRStatus); s != "1" {
		t.Fatalf("overall Status = %q, want 1", s)
	}
	resp := responseFor(root, "alice")
	if resp == nil {
		t.Fatal("no Response echoed for the query")
	}
	if s := resp.ChildText(wbxml.RRStatus); s != "1" {
		t.Errorf("response Status = %q, want 1 (resolved)", s)
	}
	if c := resp.ChildText(wbxml.RRRecipientCount); c != "1" {
		t.Errorf("RecipientCount = %q, want 1", c)
	}
	rec := resp.Child(wbxml.RRRecipient)
	if rec == nil {
		t.Fatal("resolved response carried no Recipient")
	}
	if rec.ChildText(wbxml.RREmailAddress) != testUser {
		t.Errorf("Recipient address = %q, want %q", rec.ChildText(wbxml.RREmailAddress), testUser)
	}
	if rec.ChildText(wbxml.RRType) != "1" {
		t.Errorf("Recipient Type = %q, want 1 (GAL)", rec.ChildText(wbxml.RRType))
	}
}

// TestResolveRecipientsUnresolved proves a query matching no GAL user is an
// unresolved Response with a zero count, not an error.
func TestResolveRecipientsUnresolved(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "ResolveRecipients", resolveReq("nobody-here"))
	if s := root.ChildText(wbxml.RRStatus); s != "1" {
		t.Fatalf("overall Status = %q, want 1 (the command still succeeds)", s)
	}
	resp := responseFor(root, "nobody-here")
	if resp == nil {
		t.Fatal("no Response echoed for the unresolved query")
	}
	if s := resp.ChildText(wbxml.RRStatus); s != "4" {
		t.Errorf("response Status = %q, want 4 (unresolved)", s)
	}
	if c := resp.ChildText(wbxml.RRRecipientCount); c != "0" {
		t.Errorf("RecipientCount = %q, want 0", c)
	}
	if resp.Child(wbxml.RRRecipient) != nil {
		t.Error("an unresolved response must carry no Recipient")
	}
}

// TestResolveRecipientsMultipleQueries proves each To gets its own Response, in a
// single request that mixes a resolvable and an unresolvable query.
func TestResolveRecipientsMultipleQueries(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "ResolveRecipients", resolveReq("alice", "ghost"))
	if a := responseFor(root, "alice"); a == nil || a.ChildText(wbxml.RRStatus) != "1" {
		t.Errorf("alice response missing or not resolved: %v", a)
	}
	if g := responseFor(root, "ghost"); g == nil || g.ChildText(wbxml.RRStatus) != "4" {
		t.Errorf("ghost response missing or not unresolved: %v", g)
	}
}

// TestResolveRecipientsAmbiguous proves a single query matching two GAL users
// returns both recipients with RecipientCount 2 — the multi-recipient path a
// single match would not exercise.
func TestResolveRecipientsAmbiguous(t *testing.T) {
	ts := galServer(t)

	_, root := postCommand(t, ts, "ResolveRecipients", resolveReq("al"))
	resp := responseFor(root, "al")
	if resp == nil {
		t.Fatal("no Response for the ambiguous query")
	}
	if c := resp.ChildText(wbxml.RRRecipientCount); c != "2" {
		t.Errorf("RecipientCount = %q, want 2", c)
	}
	n := 0
	for _, c := range resp.Children {
		if c.Tag == wbxml.RRRecipient {
			n++
		}
	}
	if n != 2 {
		t.Errorf("Recipient count = %d, want 2", n)
	}
}

// TestResolveRecipientsEmpty proves a request naming no recipient reports the
// protocol-error status.
func TestResolveRecipientsEmpty(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "ResolveRecipients", wbxml.Elem(wbxml.RRResolveRecipients))
	if s := root.ChildText(wbxml.RRStatus); s != "5" {
		t.Errorf("empty-request Status = %q, want 5 (protocol error)", s)
	}
}
