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

// TestRunFiltersNow proves POST /filters/run ports the old webmail's "run now":
// it applies the Inbox's stored rules to the mail already sitting in the Inbox.
// Two messages are seeded; a mark-read rule matches only one, so the handler
// must report 2 evaluated and 1 affected, and run-now must not move or delete
// the non-matching message.
func TestRunFiltersNow(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	inbox := int64(mapi.PrivateFIDInbox)
	promo := []byte("From: news@promo.com\r\nSubject: Weekly promo\r\n\r\nbuy\r\n")
	keep := []byte("From: bob@b.test\r\nSubject: lunch\r\n\r\nhi\r\n")
	if _, err := st.AppendMessage(inbox, promo, time.Now(), 0); err != nil {
		t.Fatalf("append promo: %v", err)
	}
	if _, err := st.AppendMessage(inbox, keep, time.Now(), 0); err != nil {
		t.Fatalf("append keep: %v", err)
	}
	if _, err := st.AddRule(objectstore.Rule{
		FolderID: inbox, Name: "read promos", State: mapi.RuleStateEnabled,
		Condition: objectstore.RuleSubjectContains("promo"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleMarkReadAction()}},
	}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	st.Close()

	secret := []byte("run-filters-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/filters/run", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}

	var resp struct{ Affected, Evaluated int }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Evaluated != 2 {
		t.Errorf("evaluated = %d, want 2", resp.Evaluated)
	}
	if resp.Affected != 1 {
		t.Errorf("affected = %d, want 1 (only the promo message matched)", resp.Affected)
	}

	// The non-matching message must stay in the Inbox: a mark-read rule never
	// removes mail, and run-now must not touch messages no rule matched.
	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	if msgs, _ := st2.ListMessages(inbox); len(msgs) != 2 {
		t.Errorf("inbox has %d messages, want 2 (mark-read keeps both)", len(msgs))
	}
}
