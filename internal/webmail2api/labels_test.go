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

// TestLabelsSurviveAReload is the round trip the reader depends on: labels set
// through POST /mail/labels are stored as the message's categories, so the next
// GET /mail/message must return them. The detail used to carry no labels, and a
// label added in the reader vanished on the next open.
func TestLabelsSurviveAReload(t *testing.T) {
	mbox := t.TempDir()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: bob@hermex.test\r\nSubject: labelled\r\n\r\nhi\r\n"), time.Now(), 0)
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	secret := []byte("labels-test-secret")
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", method, path, rec.Code, rec.Body.String())
		}
		return rec
	}
	labels := func() []string {
		var d struct {
			Labels []string `json:"labels"`
		}
		rec := do(http.MethodGet, "/api/v1/mail/message?id="+messageID("inbox", info.UID), "")
		if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
			t.Fatalf("decode detail: %v", err)
		}
		return d.Labels
	}

	id := messageID("inbox", info.UID)
	do(http.MethodPost, "/api/v1/mail/labels", `{"id":"`+id+`","labels":["Work","Urgent"]}`)
	if got := strings.Join(labels(), ","); got != "Work,Urgent" {
		t.Fatalf("labels after set = %q, want Work,Urgent", got)
	}
	do(http.MethodPost, "/api/v1/mail/labels", `{"id":"`+id+`","labels":[]}`)
	if got := labels(); len(got) != 0 {
		t.Fatalf("labels after clear = %q, want none", got)
	}
}
