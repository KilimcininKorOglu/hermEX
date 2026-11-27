package webmail2api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestRecallRetractsUnreadLocalCopies follows Exchange message recall: the
// author recalls a sent message; each local recipient's unread copy is hard-deleted
// (recalled), a read copy is kept (read), and an external recipient is unavailable
// (the intra-org-only limitation). A recipient cannot recall it (403, author-only).
func TestRecallRetractsUnreadLocalCopies(t *testing.T) {
	raw := []byte("From: alice@hermex.test\r\n" +
		"To: bob@hermex.test, carol@hermex.test, ghost@external.invalid, dave@other.test\r\n" +
		"Message-ID: <recall-x@hermex.test>\r\n" +
		"Subject: recall test\r\n\r\nbody\r\n")

	senderDir, bobDir, carolDir, daveDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()

	sentInfo := seedRecallCopy(t, senderDir, int64(mapi.PrivateFIDSentItems), raw, objectstore.FlagSeen, false)
	seedRecallCopy(t, bobDir, int64(mapi.PrivateFIDInbox), raw, 0, false)
	seedRecallCopy(t, carolDir, int64(mapi.PrivateFIDInbox), raw, 0, true) // carol read her copy
	seedRecallCopy(t, daveDir, int64(mapi.PrivateFIDInbox), raw, 0, false)

	// dave is hosted here too, in another organization (domain), so his copy is
	// outside the sender's reach like an external recipient's.
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: senderDir},
		"bob@hermex.test":   {MailboxPath: bobDir},
		"carol@hermex.test": {MailboxPath: carolDir},
		"dave@other.test":   {MailboxPath: daveDir},
	}
	secret := []byte("recall-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	recall := func(email, mailbox, id string) *httptest.ResponseRecorder {
		token := mustMintToken(t, secret, sessionClaims{Email: email, Mailbox: mailbox, Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/recall?id="+url.QueryEscape(id), nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	sentID := "sent:" + strconv.FormatUint(uint64(sentInfo.UID), 10)

	// A recipient cannot recall the message (author-only).
	if rec := recall("bob@hermex.test", bobDir, sentID); rec.Code != http.StatusForbidden {
		t.Fatalf("recipient recall: status %d, want 403", rec.Code)
	}
	for _, id := range []string{"sent:999999", "not-an-id", ""} {
		if rec := recall("alice@hermex.test", senderDir, id); rec.Code == http.StatusOK {
			t.Errorf("recall of %q answered 200", id)
		}
	}
	wantInboxCount(t, bobDir, 1, "bob inbox after the refused recalls")

	// The author recalls.
	type recallResponse struct {
		Recalled int            `json:"recalled"`
		Total    int            `json:"total"`
		Results  []recallResult `json:"results"`
	}
	res := okBody[recallResponse](t, "author recall", recall("alice@hermex.test", senderDir, sentID))
	wantEq(t, "recalled count", res.Recalled, 1)
	wantEq(t, "recipient total", res.Total, 4)
	status := map[string]string{}
	for _, r := range res.Results {
		status[r.Recipient] = r.Status
	}
	wantEq(t, "bob (unread, local)", status["bob@hermex.test"], "recalled")
	wantEq(t, "carol (read, local)", status["carol@hermex.test"], "read")
	wantEq(t, "ghost (external)", status["ghost@external.invalid"], "unavailable")
	wantEq(t, "dave (another organization)", status["dave@other.test"], "unavailable")

	// Bob's unread copy is gone; carol's read copy and dave's copy stay.
	wantInboxCount(t, bobDir, 0, "bob inbox (recalled)")
	wantInboxCount(t, carolDir, 1, "carol inbox (read, kept)")
	wantInboxCount(t, daveDir, 1, "dave inbox (other organization, kept)")
}

// TestARecallIntoADamagedMailboxIsRecorded: a recipient store that cannot be
// listed or read answers "unavailable", the honest outcome, and the operator's
// log is the only place the cause shows, so it must be recorded.
func TestARecallIntoADamagedMailboxIsRecorded(t *testing.T) {
	for _, table := range []string{"messages", "message_properties"} {
		t.Run(table, func(t *testing.T) {
			raw := []byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nMessage-ID: <recall-d@hermex.test>\r\nSubject: r\r\n\r\nb\r\n")
			senderDir, bobDir := t.TempDir(), t.TempDir()
			sent := seedRecallCopy(t, senderDir, int64(mapi.PrivateFIDSentItems), raw, objectstore.FlagSeen, false)
			seedRecallCopy(t, bobDir, int64(mapi.PrivateFIDInbox), raw, 0, false)
			db, err := sql.Open("sqlite", filepath.Join(bobDir, "objects.sqlite3"))
			mustNoErr(t, "open objects db", err)
			_, err = db.Exec(`DROP TABLE ` + table)
			mustNoErr(t, "drop "+table, err)
			mustNoErr(t, "close objects db", db.Close())

			accounts := directory.StaticAccounts{
				"alice@hermex.test": {MailboxPath: senderDir},
				"bob@hermex.test":   {MailboxPath: bobDir},
			}
			secret := []byte("recall-test-secret")
			srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
			sink := withSink(t)
			token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: senderDir, Exp: time.Now().Add(time.Hour).Unix()})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/recall?id=sent:"+strconv.FormatUint(uint64(sent.UID), 10), nil)
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			res := okBody[struct {
				Recalled int `json:"recalled"`
			}](t, "recall", rec)
			wantEq(t, "recalled from a damaged mailbox", res.Recalled, 0)
			if len(sink.events) == 0 {
				t.Errorf("the damaged %s table left no record", table)
			}
		})
	}
}

// seedRecallCopy files one copy of the message in a mailbox, optionally marking
// it read, and returns its index row.
func seedRecallCopy(t *testing.T, dir string, folderID int64, raw []byte, flags int64, read bool) objectstore.MessageInfo {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	defer mustClose(t, st)
	info, err := st.AppendMessage(folderID, raw, time.Now(), flags)
	mustNoErr(t, "append message", err)
	if read {
		mustNoErr(t, "set read state", st.SetMessageReadState(info.ID, true))
	}
	return info
}

// wantInboxCount checks how many messages a mailbox's inbox still holds.
func wantInboxCount(t *testing.T, dir string, want int, label string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	defer mustClose(t, st)
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list messages", err)
	wantEq(t, label, len(msgs), want)
}
