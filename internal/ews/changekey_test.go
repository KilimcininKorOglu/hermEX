package ews

import (
	"regexp"
	"testing"

	"hermex/internal/mapi"
)

// itemKeyRE captures an ItemId element's id and change key.
var itemKeyRE = regexp.MustCompile(`<(?:\w+:)?ItemId Id="([^"]+)" ChangeKey="([^"]*)"`)

// itemKey returns the first item id and change key in a response.
func itemKey(t *testing.T, out string) (string, string) {
	t.Helper()
	m := itemKeyRE.FindStringSubmatch(out)
	if len(m) != 3 {
		t.Fatalf("no ItemId with a ChangeKey in %s", out)
	}
	return m[1], m[2]
}

// TestChangeKeyFollowsTheStoredVersion holds FindItem and GetItem to one change
// key for one version of an item, and moves that key when the item is edited in
// place while its id stays the same.
func TestChangeKeyFollowsTheStoredVersion(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	id, listed := itemKey(t, fi)
	_, gi := soapPost(t, ts, getItemReq(id), true)
	if _, got := itemKey(t, gi); got != listed || got == "" {
		t.Fatalf("GetItem change key %q, FindItem %q; want one non-empty key", got, listed)
	}

	st, msgs := inboxUIDs(t, dir)
	err := st.ModifyMessageProperties(msgs[0].ID, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "edited"}})
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	_, fi = soapPost(t, ts, findItemReq("inbox"), true)
	sameID, edited := itemKey(t, fi)
	if sameID != id {
		t.Fatalf("the edit changed the item id from %s to %s", id, sameID)
	}
	if edited == listed {
		t.Errorf("change key stayed %q across an edit", edited)
	}
}
