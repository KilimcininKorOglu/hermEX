package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestRecallRetractsUnreadLocalCopies grounds Surface A of contract-map/29: the
// author recalls a sent message; each local recipient's unread copy is hard-deleted
// (recalled), a read copy is kept (read), and an external recipient is unavailable
// (the intra-org-only limitation). A recipient cannot recall it (403, author-only).
func TestRecallRetractsUnreadLocalCopies(t *testing.T) {
	raw := []byte("From: alice@hermex.test\r\n" +
		"To: bob@hermex.test, carol@hermex.test, ghost@external.invalid\r\n" +
		"Message-ID: <recall-x@hermex.test>\r\n" +
		"Subject: recall test\r\n\r\nbody\r\n")

	senderDir, bobDir, carolDir := t.TempDir(), t.TempDir(), t.TempDir()

	sentInfo := seedRecallCopy(t, senderDir, int64(mapi.PrivateFIDSentItems), raw, objectstore.FlagSeen, false)
	seedRecallCopy(t, bobDir, int64(mapi.PrivateFIDInbox), raw, 0, false)
	seedRecallCopy(t, carolDir, int64(mapi.PrivateFIDInbox), raw, 0, true) // carol read her copy

	accounts := directory.StaticAccounts{
		"bob@hermex.test":   {MailboxPath: bobDir},
		"carol@hermex.test": {MailboxPath: carolDir},
	}
	secret := []byte("recall-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	recall := func(email, mailbox, id string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: email, Mailbox: mailbox, Exp: time.Now().Add(time.Hour).Unix()})
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

	// The author recalls.
	type recallResponse struct {
		Recalled int            `json:"recalled"`
		Total    int            `json:"total"`
		Results  []recallResult `json:"results"`
	}
	res := okBody[recallResponse](t, "author recall", recall("alice@hermex.test", senderDir, sentID))
	wantEq(t, "recalled count", res.Recalled, 1)
	wantEq(t, "recipient total", res.Total, 3)
	status := map[string]string{}
	for _, r := range res.Results {
		status[r.Recipient] = r.Status
	}
	wantEq(t, "bob (unread, local)", status["bob@hermex.test"], "recalled")
	wantEq(t, "carol (read, local)", status["carol@hermex.test"], "read")
	wantEq(t, "ghost (external)", status["ghost@external.invalid"], "unavailable")

	// Bob's unread copy is gone; carol's read copy stays.
	wantInboxCount(t, bobDir, 0, "bob inbox (recalled)")
	wantInboxCount(t, carolDir, 1, "carol inbox (read, kept)")
}

// seedRecallCopy files one copy of the message in a mailbox, optionally marking
// it read, and returns its index row.
func seedRecallCopy(t *testing.T, dir string, folderID int64, raw []byte, flags int64, read bool) objectstore.MessageInfo {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	defer st.Close()
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
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list messages", err)
	wantEq(t, label, len(msgs), want)
}
