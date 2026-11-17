package activesync

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
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

// TestItemOperationsFetchMalformed confirms an unparseable id reports status 2,
// MS-ASCMD's "Protocol error - protocol violation/XML validation error" (scope
// Item or Global). 155 is a Global code reserved for a move request missing its
// Options and MoveAlways elements, so it does not describe a malformed Fetch.
func TestItemOperationsFetchMalformed(t *testing.T) {
	ts, _ := seededServer(t)
	req := wbxml.Elem(wbxml.IOItemOperations,
		wbxml.Elem(wbxml.IOFetch,
			wbxml.Str(wbxml.ASCollectionID, "notanumber"),
			wbxml.Str(wbxml.ASServerID, "x")))
	_, root := postCommand(t, ts, "ItemOperations", req)
	f := root.Child(wbxml.IOResponse).Child(wbxml.IOFetch)
	if f.ChildText(wbxml.IOStatus) != "2" {
		t.Errorf("malformed Fetch Status = %q, want 2", f.ChildText(wbxml.IOStatus))
	}
}

// TestItemOperationsEmptyFolderMalformed confirms an EmptyFolderContents with no
// usable collection id also reports status 2.
func TestItemOperationsEmptyFolderMalformed(t *testing.T) {
	ts, _ := seededServer(t)
	req := wbxml.Elem(wbxml.IOItemOperations,
		wbxml.Elem(wbxml.IOEmptyFolderContents, wbxml.Str(wbxml.ASCollectionID, "inbox")))
	_, root := postCommand(t, ts, "ItemOperations", req)
	if s := root.Child(wbxml.IOResponse).Child(wbxml.IOEmptyFolderContents).ChildText(wbxml.IOStatus); s != "2" {
		t.Errorf("malformed EmptyFolderContents Status = %q, want 2", s)
	}
}

// TestItemOperationsEmptyFolder confirms EmptyFolderContents removes every message
// in a folder and echoes its collection id.
func TestItemOperationsEmptyFolder(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 3)

	req := wbxml.Elem(wbxml.IOItemOperations,
		wbxml.Elem(wbxml.IOEmptyFolderContents,
			wbxml.Str(wbxml.ASCollectionID, inboxID())))
	_, root := postCommand(t, ts, "ItemOperations", req)
	efc := root.Child(wbxml.IOResponse).Child(wbxml.IOEmptyFolderContents)
	if efc == nil || efc.ChildText(wbxml.IOStatus) != "1" {
		t.Fatalf("EmptyFolderContents Status not 1: %+v", efc)
	}
	if efc.ChildText(wbxml.ASCollectionID) != inboxID() {
		t.Errorf("echoed CollectionId %q, want %q", efc.ChildText(wbxml.ASCollectionID), inboxID())
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox)); err != nil || len(msgs) != 0 {
		t.Errorf("inbox has %d messages after EmptyFolderContents (err %v), want 0", len(msgs), err)
	}
}

// TestItemOperationsEmptyFolderOfObjects proves EmptyFolderContents empties an
// object folder too ([MS-ASCMD] 2.2.3.56: the folder's items are deleted). An
// appointment is not in the mail index, so emptying the folder as mail reported
// success and left every appointment in place.
func TestItemOperationsEmptyFolderOfObjects(t *testing.T) {
	ts, dir := seededServer(t)
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	seedAppointment(t, dir, "Standup", start, start.Add(30*time.Minute))
	cal := strconv.Itoa(int(mapi.PrivateFIDCalendar))

	req := wbxml.Elem(wbxml.IOItemOperations,
		wbxml.Elem(wbxml.IOEmptyFolderContents, wbxml.Str(wbxml.ASCollectionID, cal)))
	_, root := postCommand(t, ts, "ItemOperations", req)
	wantEq(t, "EmptyFolderContents Status",
		root.Child(wbxml.IOResponse).Child(wbxml.IOEmptyFolderContents).ChildText(wbxml.IOStatus), "1")

	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	mustNoErr(t, "list the calendar", err)
	wantEq(t, "appointments left in the emptied calendar", len(objs), 0)
}

// TestItemOperationsEmptyFolderDeleteSubs confirms the DeleteSubFolders option also
// removes the folder's subfolders.
func TestItemOperationsEmptyFolderDeleteSubs(t *testing.T) {
	ts, dir := seededServer(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	parent := int64(mapi.PrivateFIDInbox)
	subID, err := st.CreateFolder(&parent, "Sub")
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	req := wbxml.Elem(wbxml.IOItemOperations,
		wbxml.Elem(wbxml.IOEmptyFolderContents,
			wbxml.Str(wbxml.ASCollectionID, inboxID()),
			wbxml.Elem(wbxml.IOOptions, wbxml.Elem(wbxml.IODeleteSubFolders))))
	_, root := postCommand(t, ts, "ItemOperations", req)
	if root.Child(wbxml.IOResponse).Child(wbxml.IOEmptyFolderContents).ChildText(wbxml.IOStatus) != "1" {
		t.Fatal("EmptyFolderContents with DeleteSubFolders did not succeed")
	}

	st, err = objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		if f.ID == subID {
			t.Error("subfolder survived EmptyFolderContents with DeleteSubFolders")
		}
	}
}
