package webmail2api

import (
	"encoding/json"
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

	sst, err := objectstore.Open(senderDir)
	if err != nil {
		t.Fatal(err)
	}
	sentInfo, err := sst.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, time.Now(), objectstore.FlagSeen)
	if err != nil {
		t.Fatal(err)
	}
	sst.Close()

	bst, err := objectstore.Open(bobDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bst.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	bst.Close()

	cst, err := objectstore.Open(carolDir)
	if err != nil {
		t.Fatal(err)
	}
	cInfo, err := cst.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := cst.SetMessageReadState(cInfo.ID, true); err != nil { // carol read her copy
		t.Fatal(err)
	}
	cst.Close()

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
	rec := recall("alice@hermex.test", senderDir, sentID)
	if rec.Code != http.StatusOK {
		t.Fatalf("author recall: status %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Recalled int            `json:"recalled"`
		Total    int            `json:"total"`
		Results  []recallResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, r := range res.Results {
		status[r.Recipient] = r.Status
	}
	if res.Recalled != 1 || res.Total != 3 ||
		status["bob@hermex.test"] != "recalled" ||
		status["carol@hermex.test"] != "read" ||
		status["ghost@external.invalid"] != "unavailable" {
		t.Fatalf("recall result = %+v (recalled=%d total=%d)", status, res.Recalled, res.Total)
	}

	// Bob's unread copy is gone; carol's read copy stays.
	bst2, _ := objectstore.Open(bobDir)
	defer bst2.Close()
	if msgs, _ := bst2.ListMessages(int64(mapi.PrivateFIDInbox)); len(msgs) != 0 {
		t.Errorf("bob inbox has %d messages, want 0 (recalled)", len(msgs))
	}
	cst2, _ := objectstore.Open(carolDir)
	defer cst2.Close()
	if msgs, _ := cst2.ListMessages(int64(mapi.PrivateFIDInbox)); len(msgs) != 1 {
		t.Errorf("carol inbox has %d messages, want 1 (read, kept)", len(msgs))
	}
}
