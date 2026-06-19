package activesync

import (
	"testing"

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

// TestSearchUnsupportedStore proves a Mailbox search (unsupported in v1) reports a
// request-invalid store status rather than a false empty success.
func TestSearchUnsupportedStore(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "Search", searchReq("Mailbox", "anything"))
	if s := root.ChildText(wbxml.SRStatus); s != "1" {
		t.Fatalf("overall Status = %q, want 1", s)
	}
	store := searchStore(t, root)
	if s := store.ChildText(wbxml.SRStatus); s != "2" {
		t.Errorf("unsupported-store Status = %q, want 2 (request invalid)", s)
	}
}
