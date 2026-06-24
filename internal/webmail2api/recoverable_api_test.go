package webmail2api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestRecoverableListRecoverPurge exercises the dumpster API end to end: a message
// soft-deleted from Trash is listed, recovered back into the folder, then (after a
// second soft delete) purged for good. This is the webmail2 leg of the recover UI.
func TestRecoverableListRecoverPurge(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	raw := []byte("From: a@b.test\r\nSubject: kurtarilacak\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDDeletedItems), raw, time.Now(), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.SoftDeleteMessage(int64(mapi.PrivateFIDDeletedItems), info.UID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	st.Close()

	secret := []byte("recoverable-api-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, path, nil)
		} else {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
		}
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, r)
		return rec
	}

	mid := int64(info.ID)

	// List the dumpster: 1 item carrying the message's object id and subject.
	rec := call(http.MethodGet, "/api/v1/mail/recoverable?folder=trash", "")
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), fmt.Sprintf(`"id":"%d"`, mid)) {
		t.Fatalf("list missing message id %d: %s", mid, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "kurtarilacak") {
		t.Errorf("list missing subject: %s", rec.Body.String())
	}

	// Recover it: back in the live Trash, out of the dumpster.
	rec = call(http.MethodPost, "/api/v1/mail/recoverable/recover", fmt.Sprintf(`{"folder":"trash","id":"%d"}`, mid))
	if rec.Code != 200 {
		t.Fatalf("recover: %d %s", rec.Code, rec.Body.String())
	}
	st2, _ := objectstore.Open(dir)
	live, _ := st2.ListMessages(int64(mapi.PrivateFIDDeletedItems))
	if len(live) != 1 {
		t.Errorf("trash live = %d after recover, want 1", len(live))
	}
	if dump, _ := st2.ListSoftDeleted(int64(mapi.PrivateFIDDeletedItems)); len(dump) != 0 {
		t.Errorf("dumpster = %d after recover, want 0", len(dump))
	}
	// Soft-delete the recovered copy again so it can be purged.
	_ = st2.SoftDeleteMessage(int64(mapi.PrivateFIDDeletedItems), live[0].UID)
	st2.Close()

	// Purge it: gone for good.
	rec = call(http.MethodPost, "/api/v1/mail/recoverable/purge", fmt.Sprintf(`{"folder":"trash","id":"%d"}`, mid))
	if rec.Code != 200 {
		t.Fatalf("purge: %d %s", rec.Code, rec.Body.String())
	}
	st3, _ := objectstore.Open(dir)
	defer st3.Close()
	if dump, _ := st3.ListSoftDeleted(int64(mapi.PrivateFIDDeletedItems)); len(dump) != 0 {
		t.Errorf("dumpster = %d after purge, want 0", len(dump))
	}
}
