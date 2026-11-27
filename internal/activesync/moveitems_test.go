package activesync

import (
	"strconv"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

func deletedID() string { return strconv.FormatInt(int64(mapi.PrivateFIDDeletedItems), 10) }

func moveReq(srcMsg, srcFld, dstFld string) *wbxml.Node {
	return wbxml.Elem(wbxml.MOMoves,
		wbxml.Elem(wbxml.MOMove,
			wbxml.Str(wbxml.MOSrcMsgId, srcMsg),
			wbxml.Str(wbxml.MOSrcFldId, srcFld),
			wbxml.Str(wbxml.MODstFldId, dstFld)))
}

// TestMoveItems confirms a message moves to the destination folder, the response
// reports success with the new id, and the store reflects the move.
func TestMoveItems(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	sid := firstInboxServerID(t, ts)

	_, root := postCommand(t, ts, "MoveItems", moveReq(sid, inboxID(), deletedID()))
	resp := root.Child(wbxml.MOResponse)
	if resp == nil || resp.ChildText(wbxml.MOStatus) != "3" {
		t.Fatalf("move Status = %q, want 3 (success)", resp.ChildText(wbxml.MOStatus))
	}
	dstMsg := resp.ChildText(wbxml.MODstMsgId)
	if dstMsg == "" {
		t.Fatal("move did not return a destination id")
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	if _, err := st.MessageByUID(int64(mapi.PrivateFIDDeletedItems), mustUID(t, dstMsg)); err != nil {
		t.Errorf("moved message not found in Deleted Items: %v", err)
	}
	// The source id must be a real UID: an unparsable one reads as 0, which is
	// never found, so the absence check below would pass without a move.
	if _, err := st.MessageByUID(int64(mapi.PrivateFIDInbox), mustUID(t, sid)); err == nil {
		t.Error("source message still present in Inbox after move")
	}
}

// mustUID parses a MoveItems message id as the IMAP UID it carries, failing the
// test on anything else.
func mustUID(t *testing.T, id string) uint32 {
	t.Helper()
	uid, err := strconv.ParseUint(id, 10, 32)
	if err != nil || uid == 0 {
		t.Fatalf("message id %q is not a UID", id)
	}
	return uint32(uid)
}

// TestMoveItemsSameFolder confirms a move whose source and destination match
// reports the same-source-and-destination status and leaves the message under
// its UID: a move into its own folder must not re-file it with a new one.
func TestMoveItemsSameFolder(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	sid := firstInboxServerID(t, ts)
	_, root := postCommand(t, ts, "MoveItems", moveReq(sid, inboxID(), inboxID()))
	if s := root.Child(wbxml.MOResponse).ChildText(wbxml.MOStatus); s != "4" {
		t.Errorf("same-folder move Status = %q, want 4", s)
	}
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer mustClose(t, st)
	if _, err := st.MessageByUID(int64(mapi.PrivateFIDInbox), mustUID(t, sid)); err != nil {
		t.Errorf("a same-folder move took the message off its UID: %v", err)
	}
}

// TestMoveItemsRefusesABadTarget proves the refusals [MS-ASCMD] MoveItems names
// leave the message where it was: a destination that does not exist or is not a
// folder id is Status 2, and a source folder id that is not a number is Status 1.
func TestMoveItemsRefusesABadTarget(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	sid := firstInboxServerID(t, ts)
	for _, tc := range []struct{ name, src, dst, want string }{
		{"no such destination", inboxID(), "9999999", "2"},
		{"destination not a number", inboxID(), "../13", "2"},
		{"source folder not a number", "inbox", deletedID(), "1"},
	} {
		_, root := postCommand(t, ts, "MoveItems", moveReq(sid, tc.src, tc.dst))
		if s := root.Child(wbxml.MOResponse).ChildText(wbxml.MOStatus); s != tc.want {
			t.Errorf("%s: Status = %q, want %s", tc.name, s, tc.want)
		}
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	if _, err := st.MessageByUID(int64(mapi.PrivateFIDInbox), mustUID(t, sid)); err != nil {
		t.Errorf("a refused move took the message out of the Inbox: %v", err)
	}
}

// TestMoveItemsInvalidSource confirms a move of an unknown or malformed message id
// reports the invalid-source status ([MS-ASCMD] 2.2.3.177.10 Status 1) and moves
// nothing.
func TestMoveItemsInvalidSource(t *testing.T) {
	ts, dir := seededServer(t)
	for _, src := range []string{"999999", "-1", "0", "99999999999999999999", "abc", ""} {
		_, root := postCommand(t, ts, "MoveItems", moveReq(src, inboxID(), deletedID()))
		if s := root.Child(wbxml.MOResponse).ChildText(wbxml.MOStatus); s != "1" {
			t.Errorf("source %q: move Status = %q, want 1", src, s)
		}
	}
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer mustClose(t, st)
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDDeletedItems))
	mustNoErr(t, "list Deleted Items", err)
	wantEq(t, "messages in Deleted Items after a refused move", len(msgs), 0)
}

// TestMoveItemsReportsAStoreFailureAsRetryable proves a move the store cannot
// carry out answers Status 7, "source or destination item was locked", the
// transient condition [MS-ASCMD] 2.2.3.177.10 tells the client to retry. Status 5
// means the request named more than one DstFldId, which a store failure is not.
func TestMoveItemsReportsAStoreFailureAsRetryable(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	mustNoErr(t, "open the store", err)
	mustNoErr(t, "close the store", st.Close())
	resp := moveOne(st, moveReq("1", inboxID(), deletedID()).Child(wbxml.MOMove))
	wantEq(t, "Status", resp.ChildText(wbxml.MOStatus), "7")
}
