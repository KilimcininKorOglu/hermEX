package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestCopyMessageToFolder proves POST /mail/copy ports the old webmail's
// copy-to-folder: the message lands in the target folder while the original
// stays put (a copy, not a move), and the copy keeps the source flags.
func TestCopyMessageToFolder(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.CreateFolder(nil, "Project"); err != nil {
		t.Fatalf("create folder: %v", err)
	}
	inbox := int64(mapi.PrivateFIDInbox)
	raw := []byte("From: a@b.test\r\nSubject: keep me\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(inbox, raw, time.Now(), objectstore.FlagSeen)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	uid := info.UID
	st.Close()

	secret := []byte("copy-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	body := strings.NewReader(`{"id":"inbox:` + strconv.FormatUint(uint64(uid), 10) + `","to":"Project"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/copy", body)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("copy: %d %s", rec.Code, rec.Body.String())
	}

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	// A copy leaves the original in the inbox.
	if msgs, _ := st2.ListMessages(inbox); len(msgs) != 1 {
		t.Errorf("inbox has %d messages, want 1 (original kept)", len(msgs))
	}
	// The copy lands in Project and keeps the \Seen flag it was copied with.
	pfid, ok := folderByName(st2, "Project")
	if !ok {
		t.Fatalf("Project folder vanished")
	}
	copies, _ := st2.ListMessages(pfid)
	if len(copies) != 1 {
		t.Fatalf("Project has %d messages, want 1 (the copy)", len(copies))
	}
	if copies[0].Flags&objectstore.FlagSeen == 0 {
		t.Errorf("copied message lost its \\Seen flag")
	}
}
