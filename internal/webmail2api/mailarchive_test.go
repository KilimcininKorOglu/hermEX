package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestMoveToArchiveFilesTheMail proves the reader's "Archive" move target, the
// slug "archive", files the mail into the mailbox's built-in Archive folder and
// that the Archive folder lists it. The move answered "unknown target folder"
// for every message before, so the menu entry did nothing.
func TestMoveToArchiveFilesTheMail(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	raw := []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Keep\r\n" +
		"Date: Mon, 01 Jun 2026 10:00:00 +0000\r\n\r\nbody\r\n")
	_, err = st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	mustNoErr(t, "append", err)
	mustClose(t, st)

	secret := []byte("archive-move-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/move", strings.NewReader(`{"id":"inbox:1","to":"archive"}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	wantEq(t, "move status", rec.Code, http.StatusOK)

	st, err = objectstore.Open(dir)
	mustNoErr(t, "reopen the store", err)
	defer mustClose(t, st)
	archived, err := st.ListMessages(int64(mapi.PrivateFIDArchive))
	mustNoErr(t, "list Archive", err)
	wantEq(t, "messages in Archive", len(archived), 1)
	inbox, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list Inbox", err)
	wantEq(t, "messages left in the Inbox", len(inbox), 0)
}

// TestARefusedMoveLeavesTheMailInPlace: a move to a folder that does not exist,
// or of a message that does not exist, is refused and moves nothing.
func TestARefusedMoveLeavesTheMailInPlace(t *testing.T) {
	mbox := freshMailbox(t)
	st := openMailbox(t, mbox)
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: bob@hermex.test\r\nSubject: Stay\r\n\r\nbody\r\n"), time.Now(), 0)
	mustNoErr(t, "append", err)
	do := apiHarnessFor(t, mbox)
	id := messageID("inbox", info.UID)
	for _, body := range []string{
		`{"id":"` + id + `","to":"no-such-folder"}`,
		`{"id":"` + id + `","to":""}`,
		`{"id":"inbox:999999","to":"archive"}`,
		`{"id":"not-an-id","to":"archive"}`,
	} {
		if rec := do(http.MethodPost, "/api/v1/mail/move", body); rec.Code == http.StatusOK {
			t.Errorf("move %s answered 200", body)
		}
	}
	inbox, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list Inbox", err)
	wantEq(t, "messages left in the Inbox", len(inbox), 1)
	archived, err := st.ListMessages(int64(mapi.PrivateFIDArchive))
	mustNoErr(t, "list Archive", err)
	wantEq(t, "messages in Archive", len(archived), 0)
}
