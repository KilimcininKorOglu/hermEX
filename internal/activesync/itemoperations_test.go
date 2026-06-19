package activesync

import (
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/wbxml"
)

// firstInboxServerID syncs the Inbox and returns the server id of its first item.
func firstInboxServerID(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	postCommand(t, ts, "Sync", syncReq("0", ""))
	_, root := postCommand(t, ts, "Sync", syncReq("1", ""))
	cmds := respColl(t, root).Child(wbxml.ASCommands)
	if cmds == nil || len(cmds.Children) == 0 {
		t.Fatal("Sync returned no Add commands to fetch")
	}
	return cmds.Children[0].ChildText(wbxml.ASServerID)
}

// fetchOp wraps a single Fetch in an ItemOperations document.
func fetchOp(serverID string) *wbxml.Node {
	return wbxml.Elem(wbxml.IOItemOperations,
		wbxml.Elem(wbxml.IOFetch,
			wbxml.Str(wbxml.IOStore, "Mailbox"),
			wbxml.Str(wbxml.ASCollectionID, inboxID()),
			wbxml.Str(wbxml.ASServerID, serverID)))
}

// TestItemOperationsFetch confirms a Fetch returns the full message as a MIME body.
func TestItemOperationsFetch(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	sid := firstInboxServerID(t, ts)

	_, root := postCommand(t, ts, "ItemOperations", fetchOp(sid))
	if root.ChildText(wbxml.IOStatus) != "1" {
		t.Errorf("ItemOperations Status = %q, want 1", root.ChildText(wbxml.IOStatus))
	}
	f := root.Child(wbxml.IOResponse).Child(wbxml.IOFetch)
	if f == nil || f.ChildText(wbxml.IOStatus) != "1" {
		t.Fatalf("Fetch Status not 1: %+v", f)
	}
	if f.ChildText(wbxml.ASServerID) != sid {
		t.Errorf("Fetch echoed ServerID %q, want %q", f.ChildText(wbxml.ASServerID), sid)
	}
	body := f.Child(wbxml.IOProperties).Child(wbxml.ABBody)
	if body == nil || body.ChildText(wbxml.ABType) != "4" {
		t.Fatalf("Fetch body missing or not MIME (Type 4): %+v", body)
	}
	data := body.Child(wbxml.ABData)
	if data == nil || !strings.Contains(string(data.Opaque), "Subject: Message 1") {
		t.Error("fetched body does not carry the message MIME")
	}
}

// TestItemOperationsFetchNotFound confirms an unknown message reports a conversion
// failure with no body.
func TestItemOperationsFetchNotFound(t *testing.T) {
	ts, _ := seededServer(t)
	_, root := postCommand(t, ts, "ItemOperations", fetchOp("999999"))
	f := root.Child(wbxml.IOResponse).Child(wbxml.IOFetch)
	if f.ChildText(wbxml.IOStatus) != "14" {
		t.Errorf("missing-message Fetch Status = %q, want 14", f.ChildText(wbxml.IOStatus))
	}
	if f.Child(wbxml.IOProperties) != nil {
		t.Error("a failed Fetch must not carry Properties")
	}
}

// TestItemOperationsFetchMalformed confirms an unparseable id reports a protocol
// error.
func TestItemOperationsFetchMalformed(t *testing.T) {
	ts, _ := seededServer(t)
	req := wbxml.Elem(wbxml.IOItemOperations,
		wbxml.Elem(wbxml.IOFetch,
			wbxml.Str(wbxml.ASCollectionID, "notanumber"),
			wbxml.Str(wbxml.ASServerID, "x")))
	_, root := postCommand(t, ts, "ItemOperations", req)
	f := root.Child(wbxml.IOResponse).Child(wbxml.IOFetch)
	if f.ChildText(wbxml.IOStatus) != "155" {
		t.Errorf("malformed Fetch Status = %q, want 155", f.ChildText(wbxml.IOStatus))
	}
}
