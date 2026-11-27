package webmail2api

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
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
	mustClose(t, st)

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
	if len(live) != 1 {
		t.Fatalf("live trash messages after recover = %d, want 1", len(live))
	}
	wantEq(t, "dumpster items after recover", len(mustListDumpster(t, st2, trash)), 0)
	// Soft-delete the recovered copy again so it can be purged.
	mustNoErr(t, "soft-delete the recovered copy", st2.SoftDeleteMessage(trash, live[0].UID))
	mustClose(t, st2)

	// Purge it: gone for good.
	wantStatus(t, "purge",
		call(http.MethodPost, "/api/v1/mail/recoverable/purge", fmt.Sprintf(`{"folder":"trash","id":"%d"}`, mid)), http.StatusOK)
	st3, err := objectstore.Open(dir)
	mustNoErr(t, "reopen mailbox", err)
	defer mustClose(t, st3)
	wantEq(t, "dumpster items after purge", len(mustListDumpster(t, st3, trash)), 0)
	gone, err := st3.ListMessages(trash)
	mustNoErr(t, "list trash after purge", err)
	wantEq(t, "live trash messages after purge", len(gone), 0)
}

// TestRecoverableRefusesWhatIsNotInTheDumpster: purge and recover act only on
// a soft-deleted item. A live message's id, an unknown id and a malformed id
// are refused, and the live message is untouched.
func TestRecoverableRefusesWhatIsNotInTheDumpster(t *testing.T) {
	trash := int64(mapi.PrivateFIDDeletedItems)
	dir := freshMailbox(t)
	st := openMailbox(t, dir)
	info, err := st.AppendMessage(trash, []byte("From: a@b.test\r\nSubject: live\r\n\r\nhi\r\n"), time.Now(), 0)
	mustNoErr(t, "append", err)
	call := apiHarnessFor(t, dir)
	for _, path := range []string{"/api/v1/mail/recoverable/purge", "/api/v1/mail/recoverable/recover"} {
		for _, id := range []string{fmt.Sprint(info.ID), "999999", "abc"} {
			if rec := call(http.MethodPost, path, `{"folder":"trash","id":"`+id+`"}`); rec.Code == http.StatusOK {
				t.Errorf("%s of %s answered 200", path, id)
			}
		}
	}
	live, err := st.ListMessages(trash)
	mustNoErr(t, "list trash", err)
	wantEq(t, "live trash messages after the refused calls", len(live), 1)
}

// TestADamagedDumpsterIsAServerFault: when the store cannot be read, list,
// recover and purge answer 500 and record the cause, instead of a 404 that
// tells the user the item is gone.
func TestADamagedDumpsterIsAServerFault(t *testing.T) {
	trash := int64(mapi.PrivateFIDDeletedItems)
	dir := freshMailbox(t)
	st := openMailbox(t, dir)
	info, err := st.AppendMessage(trash, []byte("From: a@b.test\r\nSubject: s\r\n\r\nhi\r\n"), time.Now(), 0)
	mustNoErr(t, "append", err)
	mustNoErr(t, "soft delete", st.SoftDeleteMessage(trash, info.UID))
	db, err := sql.Open("sqlite", filepath.Join(dir, "objects.sqlite3"))
	mustNoErr(t, "open objects db", err)
	_, err = db.Exec(`DROP TABLE messages`)
	mustNoErr(t, "drop messages", err)
	mustNoErr(t, "close objects db", db.Close())

	call := apiHarnessFor(t, dir)
	body := fmt.Sprintf(`{"folder":"trash","id":"%d"}`, info.ID)
	for _, c := range []struct{ method, path, body, op string }{
		{http.MethodGet, "/api/v1/mail/recoverable?folder=trash", "", "recoverable-list"},
		{http.MethodPost, "/api/v1/mail/recoverable/recover", body, "recoverable-recover"},
		{http.MethodPost, "/api/v1/mail/recoverable/purge", body, "recoverable-purge"},
	} {
		sink := withSink(t)
		wantStatus(t, c.path, call(c.method, c.path, c.body), http.StatusInternalServerError)
		if _, ok := sink.find(c.op); !ok {
			t.Errorf("%s: the store failure left no %s record", c.path, c.op)
		}
	}
}

// mustListDumpster lists a folder's soft-deleted items.
func mustListDumpster(t *testing.T, st *objectstore.Store, folderID int64) []objectstore.SoftDeletedMessage {
	t.Helper()
	dump, err := st.ListSoftDeleted(folderID)
	mustNoErr(t, "list soft deleted", err)
	return dump
}
