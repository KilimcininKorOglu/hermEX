package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// TestDraftKeepsItsBcc proves a saved draft keeps every recipient list. The draft
// used to be built by the send path, which leaves Bcc out of the header on
// purpose, so a Bcc recipient was gone the moment the draft was saved and the
// reopened draft had no way to show or send to it.
func TestDraftKeepsItsBcc(t *testing.T) {
	mbox := t.TempDir()
	secret := []byte("draft-bcc-test-secret")
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path, body string) []byte {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", method, path, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}

	var saved struct {
		ID string `json:"id"`
	}
	body := do(http.MethodPost, "/api/v1/mail/draft",
		`{"to":["bob@hermex.test"],"cc":["carol@hermex.test"],"bcc":["dave@hermex.test"],"subject":"plan","body":"x"}`)
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}

	var d struct {
		To  []string `json:"to"`
		Cc  []string `json:"cc"`
		Bcc []string `json:"bcc"`
	}
	if err := json.Unmarshal(do(http.MethodGet, "/api/v1/mail/message?id="+saved.ID, ""), &d); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(d.To, ",") + "|" + strings.Join(d.Cc, ",") + "|" + strings.Join(d.Bcc, ",")
	if want := "bob@hermex.test|carol@hermex.test|dave@hermex.test"; got != want {
		t.Fatalf("reopened draft recipients = %q, want %q", got, want)
	}
}
