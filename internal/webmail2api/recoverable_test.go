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
	st.Close()

	secret := []byte("recoverable-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
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
	defer st2.Close()
	if msgs, _ := st2.ListMessages(int64(mapi.PrivateFIDDeletedItems)); len(msgs) != 0 {
		t.Errorf("trash has %d live messages after delete, want 0", len(msgs))
	}
	dump, _ := st2.ListSoftDeleted(int64(mapi.PrivateFIDDeletedItems))
	if len(dump) != 1 {
		t.Fatalf("dumpster has %d items, want 1 (delete-from-trash must be recoverable)", len(dump))
	}
	if dump[0].Subject != "silinecek" {
		t.Errorf("dumpster item subject = %q, want %q", dump[0].Subject, "silinecek")
	}
}
