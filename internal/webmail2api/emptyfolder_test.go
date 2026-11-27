package webmail2api

import (
	"net/http"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestEmptyFolderMovesToTrashAndDumpstersJunk proves POST /folders/{name}/empty
// moves an ordinary folder's messages to Deleted Items, while emptying Junk (or
// Trash) sends them to the Recoverable Items dumpster (soft delete) rather than
// purging them outright, so they stay recoverable until retention.
func TestEmptyFolderMovesToTrashAndDumpstersJunk(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	cfid, err := st.CreateFolder(nil, "Project")
	mustNoErr(t, "create folder", err)
	raw := []byte("From: a@b.test\r\nSubject: x\r\n\r\nhi\r\n")
	for _, fid := range []int64{cfid, cfid, int64(mapi.PrivateFIDJunk)} {
		_, err = st.AppendMessage(fid, raw, time.Now(), 0)
		mustNoErr(t, "seed a message", err)
	}
	mustClose(t, st)

	do := apiHarnessFor(t, dir)
	wantStatus(t, "empty custom", do(http.MethodPost, "/api/v1/folders/"+customSlug(cfid)+"/empty", ""), http.StatusOK)
	wantStatus(t, "empty spam", do(http.MethodPost, "/api/v1/folders/spam/empty", ""), http.StatusOK)

	st2, err := objectstore.Open(dir)
	mustNoErr(t, "reopen mailbox", err)
	defer mustClose(t, st2)
	wantEq(t, "custom folder messages (moved to trash)", len(mustList(t, st2, cfid)), 0)
	wantEq(t, "trash messages", len(mustList(t, st2, int64(mapi.PrivateFIDDeletedItems))), 2)
	wantEq(t, "junk live messages (emptied)", len(mustList(t, st2, int64(mapi.PrivateFIDJunk))), 0)
	// The emptied Junk message is in the dumpster, not purged: recoverable.
	wantEq(t, "junk dumpster items (soft-deleted, recoverable)",
		len(mustListDumpster(t, st2, int64(mapi.PrivateFIDJunk))), 1)
}

// TestEmptyingAnUnknownFolderTouchesNothing: a folder slug that names no folder
// is refused and every message stays where it was.
func TestEmptyingAnUnknownFolderTouchesNothing(t *testing.T) {
	dir := freshMailbox(t)
	st := openMailbox(t, dir)
	_, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: a@b.test\r\nSubject: x\r\n\r\nhi\r\n"), time.Now(), 0)
	mustNoErr(t, "seed a message", err)
	do := apiHarnessFor(t, dir)
	for _, slug := range []string{"no-such-folder", customSlug(99999)} {
		if rec := do(http.MethodPost, "/api/v1/folders/"+slug+"/empty", ""); rec.Code == http.StatusOK {
			t.Errorf("empty %s answered 200", slug)
		}
	}
	wantEq(t, "inbox after the refused empties", len(mustList(t, st, int64(mapi.PrivateFIDInbox))), 1)
	wantEq(t, "trash after the refused empties", len(mustList(t, st, int64(mapi.PrivateFIDDeletedItems))), 0)
}

// TestEmptyMessagesReportsAFailure proves a store write that fails is returned,
// not swallowed, and that the count covers only what was removed: a folder
// reported as emptied must not still hold the messages. A closed store fails
// every write, for the move to Deleted Items and the dumpster alike.
func TestEmptyMessagesReportsAFailure(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	mustNoErr(t, "open mailbox", err)
	raw := []byte("From: a@b.test\r\nSubject: x\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	mustNoErr(t, "seed a message", err)
	msgs := []objectstore.MessageInfo{info}
	mustClose(t, st)

	for _, toDumpster := range []bool{false, true} {
		n, err := emptyMessages(st, int64(mapi.PrivateFIDInbox), msgs, toDumpster)
		if err == nil {
			t.Errorf("dumpster=%v: a failed write reported no error", toDumpster)
		}
		wantEq(t, "messages reported removed", n, 0)
	}
}

// mustList enumerates a folder's live messages.
func mustList(t *testing.T, st *objectstore.Store, folderID int64) []objectstore.MessageInfo {
	t.Helper()
	msgs, err := st.ListMessages(folderID)
	mustNoErr(t, "list messages", err)
	return msgs
}
