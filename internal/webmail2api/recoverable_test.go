package webmail2api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestDeleteFromTrashGoesToDumpster proves DELETE /mail/delete on a message in
// Trash soft-deletes it into the Recoverable Items dumpster rather than purging it:
// it leaves the live Trash listing but stays recoverable. Deleting from a normal
// folder still just moves to Trash. This is the webmail2 leg of routing every
// hard-delete into the dumpster.
func TestDeleteFromTrashGoesToDumpster(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	raw := []byte("From: a@b.test\r\nSubject: silinecek\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDDeletedItems), raw, time.Now(), 0)
	if err != nil {
		t.Fatalf("append to trash: %v", err)
	}
	mustClose(t, st)

	secret := []byte("recoverable-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/mail/delete?id=trash:%d", info.UID), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer mustClose(t, st2)
	msgs, err := st2.ListMessages(int64(mapi.PrivateFIDDeletedItems))
	mustNoErr(t, "list trash", err)
	if len(msgs) != 0 {
		t.Errorf("trash has %d live messages after delete, want 0", len(msgs))
	}
	dump, err := st2.ListSoftDeleted(int64(mapi.PrivateFIDDeletedItems))
	mustNoErr(t, "list dumpster", err)
	if len(dump) != 1 {
		t.Fatalf("dumpster has %d items, want 1 (delete-from-trash must be recoverable)", len(dump))
	}
	if dump[0].Subject != "silinecek" {
		t.Errorf("dumpster item subject = %q, want %q", dump[0].Subject, "silinecek")
	}
}

// TestDeleteFromAFolderMovesToTrash: deleting from an ordinary folder moves the
// mail to Trash and puts nothing in the dumpster; an unknown or malformed id is
// refused and moves nothing.
func TestDeleteFromAFolderMovesToTrash(t *testing.T) {
	dir := freshMailbox(t)
	st := openMailbox(t, dir)
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: a@b.test\r\nSubject: tasinacak\r\n\r\nhi\r\n"), time.Now(), 0)
	mustNoErr(t, "append", err)
	do := apiHarnessFor(t, dir)
	wantStatus(t, "delete of an unknown uid", do(http.MethodDelete, "/api/v1/mail/delete?id=inbox:999999", ""), http.StatusNotFound)
	wantStatus(t, "delete of an unknown trash uid", do(http.MethodDelete, "/api/v1/mail/delete?id=trash:999999", ""), http.StatusNotFound)
	for _, id := range []string{"not-an-id", ""} {
		if rec := do(http.MethodDelete, "/api/v1/mail/delete?id="+id, ""); rec.Code == http.StatusOK {
			t.Errorf("delete of %q answered 200", id)
		}
	}
	trash, err := st.ListMessages(int64(mapi.PrivateFIDDeletedItems))
	mustNoErr(t, "list trash", err)
	wantEq(t, "trash after the refused deletes", len(trash), 0)

	wantStatus(t, "delete", do(http.MethodDelete, fmt.Sprintf("/api/v1/mail/delete?id=inbox:%d", info.UID), ""), http.StatusOK)
	trash, err = st.ListMessages(int64(mapi.PrivateFIDDeletedItems))
	mustNoErr(t, "list trash", err)
	if len(trash) != 1 || trash[0].Subject != "tasinacak" {
		t.Errorf("trash = %+v, want the moved mail", trash)
	}
	inbox, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list inbox", err)
	wantEq(t, "inbox after delete", len(inbox), 0)
	wantEq(t, "dumpster after an ordinary delete", len(mustListDumpster(t, st, int64(mapi.PrivateFIDInbox))), 0)
}
