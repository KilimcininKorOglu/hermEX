package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestMailDetailReportsWhetherANoteCanBeAdded proves the reader learns whether a
// mail can carry a note. A note links to its mail by the Message-ID, so a mail
// without one refuses the note; the reader offered the action anyway and the
// user met a generic error.
func TestMailDetailReportsWhetherANoteCanBeAdded(t *testing.T) {
	mbox := t.TempDir()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	withID, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: bob@hermex.test\r\nMessage-ID: <n1@hermex.test>\r\nSubject: linked\r\n\r\nhi\r\n"), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	withoutID, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: bob@hermex.test\r\nSubject: bare\r\n\r\nhi\r\n"), time.Now(), 0)
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	secret := []byte("annotatable-test-secret")
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	annotatable := func(uid uint32) bool {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/message?id="+messageID("inbox", uid), nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET message = %d: %s", rec.Code, rec.Body.String())
		}
		var d struct {
			Annotatable bool `json:"annotatable"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
			t.Fatalf("decode detail: %v", err)
		}
		return d.Annotatable
	}

	if !annotatable(withID.UID) {
		t.Error("a mail with a Message-ID reads as not annotatable")
	}
	if annotatable(withoutID.UID) {
		t.Error("a mail without a Message-ID reads as annotatable, so the reader offers a note the server refuses")
	}
}
