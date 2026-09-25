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

// TestSendingADraftRemovesIt proves a draft that is sent leaves the Drafts
// folder. The send carried no link to the draft it was composed from, so the
// message stayed in Drafts as if it were still unsent, and sending it again from
// there delivered it twice.
func TestSendingADraftRemovesIt(t *testing.T) {
	alice, bob := t.TempDir(), t.TempDir()
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {Password: "pw", MailboxPath: alice},
		"bob@hermex.test":   {Password: "pw", MailboxPath: bob},
	}
	secret := []byte("send-draft-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: alice, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, body string) []byte {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}

	var draft struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(post("/api/v1/mail/draft", `{"to":["bob@hermex.test"],"subject":"plan","body":"x"}`), &draft); err != nil {
		t.Fatal(err)
	}
	post("/api/v1/mail/send", `{"to":["bob@hermex.test"],"subject":"plan","body":"x","draftId":"`+draft.ID+`"}`)

	st, err := objectstore.Open(alice)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	drafts, err := st.ListMessages(int64(mapi.PrivateFIDDraft))
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 0 {
		t.Errorf("Drafts holds %d messages after the draft was sent, want 0", len(drafts))
	}
	sent, err := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Errorf("Sent Items holds %d messages, want the one sent copy", len(sent))
	}
}
