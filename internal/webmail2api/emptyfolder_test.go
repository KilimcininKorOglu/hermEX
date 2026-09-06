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
	_, err = st.CreateFolder(nil, "Project")
	mustNoErr(t, "create folder", err)
	cfid, ok := folderByName(st, "Project")
	if !ok {
		t.Fatalf("custom folder not found after create")
	}
	raw := []byte("From: a@b.test\r\nSubject: x\r\n\r\nhi\r\n")
	_, _ = st.AppendMessage(cfid, raw, time.Now(), 0)
	_, _ = st.AppendMessage(cfid, raw, time.Now(), 0)
	_, _ = st.AppendMessage(int64(mapi.PrivateFIDJunk), raw, time.Now(), 0)
	st.Close()

	do := apiHarnessFor(t, dir)
	wantStatus(t, "empty custom", do(http.MethodPost, "/api/v1/folders/Project/empty", ""), http.StatusOK)
	wantStatus(t, "empty spam", do(http.MethodPost, "/api/v1/folders/spam/empty", ""), http.StatusOK)

	st2, err := objectstore.Open(dir)
	mustNoErr(t, "reopen mailbox", err)
	defer st2.Close()
	wantEq(t, "custom folder messages (moved to trash)", len(mustList(t, st2, cfid)), 0)
	wantEq(t, "trash messages", len(mustList(t, st2, int64(mapi.PrivateFIDDeletedItems))), 2)
	wantEq(t, "junk live messages (emptied)", len(mustList(t, st2, int64(mapi.PrivateFIDJunk))), 0)
	// The emptied Junk message is in the dumpster, not purged: recoverable.
	wantEq(t, "junk dumpster items (soft-deleted, recoverable)",
		len(mustListDumpster(t, st2, int64(mapi.PrivateFIDJunk))), 1)
}

// mustList enumerates a folder's live messages.
func mustList(t *testing.T, st *objectstore.Store, folderID int64) []objectstore.MessageInfo {
	t.Helper()
	msgs, err := st.ListMessages(folderID)
	mustNoErr(t, "list messages", err)
	return msgs
}
