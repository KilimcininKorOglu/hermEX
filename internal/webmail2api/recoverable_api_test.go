package webmail2api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestRecoverableListRecoverPurge exercises the dumpster API end to end: a message
// soft-deleted from Trash is listed, recovered back into the folder, then (after a
// second soft delete) purged for good. This is the webmail2 leg of the recover UI.
func TestRecoverableListRecoverPurge(t *testing.T) {
	trash := int64(mapi.PrivateFIDDeletedItems)
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	raw := []byte("From: a@b.test\r\nSubject: kurtarilacak\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(trash, raw, time.Now(), 0)
	mustNoErr(t, "append", err)
	mustNoErr(t, "soft delete", st.SoftDeleteMessage(trash, info.UID))
	st.Close()

	call := apiHarnessFor(t, dir)
	mid := int64(info.ID)

	// List the dumpster: 1 item carrying the message's object id and subject.
	rec := call(http.MethodGet, "/api/v1/mail/recoverable?folder=trash", "")
	wantStatus(t, "list", rec, http.StatusOK)
	wantContains(t, "dumpster listing", rec.Body.String(), fmt.Sprintf(`"id":"%d"`, mid))
	wantContains(t, "dumpster listing", rec.Body.String(), "kurtarilacak")

	// Recover it: back in the live Trash, out of the dumpster.
	wantStatus(t, "recover",
		call(http.MethodPost, "/api/v1/mail/recoverable/recover", fmt.Sprintf(`{"folder":"trash","id":"%d"}`, mid)), http.StatusOK)
	st2, err := objectstore.Open(dir)
	mustNoErr(t, "reopen mailbox", err)
	live, err := st2.ListMessages(trash)
	mustNoErr(t, "list messages", err)
	wantEq(t, "live trash messages after recover", len(live), 1)
	wantEq(t, "dumpster items after recover", len(mustListDumpster(t, st2, trash)), 0)
	// Soft-delete the recovered copy again so it can be purged.
	_ = st2.SoftDeleteMessage(trash, live[0].UID)
	st2.Close()

	// Purge it: gone for good.
	wantStatus(t, "purge",
		call(http.MethodPost, "/api/v1/mail/recoverable/purge", fmt.Sprintf(`{"folder":"trash","id":"%d"}`, mid)), http.StatusOK)
	st3, err := objectstore.Open(dir)
	mustNoErr(t, "reopen mailbox", err)
	defer st3.Close()
	wantEq(t, "dumpster items after purge", len(mustListDumpster(t, st3, trash)), 0)
}

// mustListDumpster lists a folder's soft-deleted items.
func mustListDumpster(t *testing.T, st *objectstore.Store, folderID int64) []objectstore.SoftDeletedMessage {
	t.Helper()
	dump, err := st.ListSoftDeleted(folderID)
	mustNoErr(t, "list soft deleted", err)
	return dump
}
