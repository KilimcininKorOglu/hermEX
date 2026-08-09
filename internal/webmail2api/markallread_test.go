package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// sharedInboxFor provisions a shared mailbox holding two unread inbox messages
// and grants the given user the given rights on the Inbox.
func sharedInboxFor(t *testing.T, user string, rights uint32) (path string) {
	t.Helper()
	path = t.TempDir()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetStoreOwners([]string{"owner@hermex.test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ModifyPermissions(int64(mapi.PrivateFIDInbox), false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: user, Rights: rights},
	}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		raw := "From: s@hermex.test\r\nSubject: unread " + string(rune('a'+i)) + "\r\n\r\nbody"
		if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// unreadCount reports how many inbox messages are still unread.
func unreadCount(t *testing.T, path string) int {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range msgs {
		if m.Flags&objectstore.FlagSeen == 0 {
			n++
		}
	}
	return n
}

// markAllRead posts the request as user against the given owner's mailbox.
func markAllRead(t *testing.T, srv *Server, secret []byte, user, ownMailbox, owner string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := mintToken(secret, sessionClaims{Email: user, Mailbox: ownMailbox, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	target := "/api/v1/mail/mark-all-read"
	if owner != "" {
		target += "?owner=" + owner
	}
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"folder":"inbox"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestMarkAllReadRefusesAReadOnlyGrantee proves a read-only grant on someone
// else's folder does not carry the right to clear their unread state. Marking
// everything read is a bulk write, so it takes the write gate the other shared
// mutations take, not the read gate that merely lets the grantee look.
func TestMarkAllReadRefusesAReadOnlyGrantee(t *testing.T) {
	shared := sharedInboxFor(t, "reader@hermex.test", mapi.RightsReviewer) // read + visible only
	own := t.TempDir()
	accounts := directory.StaticAccounts{
		"reader@hermex.test": {Password: "pw", MailboxPath: own},
		"team@hermex.test":   {Shared: true, MailboxPath: shared},
	}
	secret := []byte("mark-all-read-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)

	rec := markAllRead(t, srv, secret, "reader@hermex.test", own, "team@hermex.test")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if got := unreadCount(t, shared); got != 2 {
		t.Errorf("%d messages left unread, want both: a read-only grantee changed the owner's state", got)
	}
}

// TestMarkAllReadAllowsAWriteGrantee is the negative control: the grant that does
// carry write rights still works, so the gate refuses only what it should.
func TestMarkAllReadAllowsAWriteGrantee(t *testing.T) {
	shared := sharedInboxFor(t, "editor@hermex.test", mapi.RightsEditor)
	own := t.TempDir()
	accounts := directory.StaticAccounts{
		"editor@hermex.test": {Password: "pw", MailboxPath: own},
		"team@hermex.test":   {Shared: true, MailboxPath: shared},
	}
	secret := []byte("mark-all-read-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)

	rec := markAllRead(t, srv, secret, "editor@hermex.test", own, "team@hermex.test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Marked int `json:"marked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Marked != 2 {
		t.Errorf("marked = %d, want 2", got.Marked)
	}
	if n := unreadCount(t, shared); n != 0 {
		t.Errorf("%d messages still unread after a write-granted pass", n)
	}
}

// TestMarkAllReadOwnMailboxStillWorks proves the caller's own mailbox is
// unaffected by the gate: no grant is involved there.
func TestMarkAllReadOwnMailboxStillWorks(t *testing.T) {
	own := sharedInboxFor(t, "nobody@hermex.test", 0)
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: own}}
	secret := []byte("mark-all-read-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)

	rec := markAllRead(t, srv, secret, "alice@hermex.test", own, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if n := unreadCount(t, own); n != 0 {
		t.Errorf("%d messages still unread in the caller's own mailbox", n)
	}
}
