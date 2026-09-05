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

// runFilters posts a scoped run and returns the decoded counts and the status.
func runFilters(t *testing.T, srv *Server, token, body string) (int, struct{ Affected, Evaluated int }) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/filters/run", nil)
	} else {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/filters/run", strings.NewReader(body))
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var out struct{ Affected, Evaluated int }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestRunFiltersScope covers the two selections the endpoint accepts. A user who
// writes a rule after the fact needs to run it over the folder the old mail is in,
// and a user who fixes one rule needs that rule applied without the others.
func TestRunFiltersScope(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	inbox := int64(mapi.PrivateFIDInbox)
	junk := int64(mapi.PrivateFIDJunk)
	promo := []byte("From: news@promo.com\r\nSubject: Weekly promo\r\n\r\nbuy\r\n")
	if _, err := st.AppendMessage(inbox, promo, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	junkMsg, err := st.AppendMessage(junk, promo, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRule(objectstore.Rule{
		FolderID: inbox, Name: "read promos", Sequence: 0, State: mapi.RuleStateEnabled,
		Condition: objectstore.RuleSubjectContains("promo"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleMarkReadAction()}},
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	secret := []byte("run-filters-scope-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})

	t.Run("named folder", func(t *testing.T) {
		code, out := runFilters(t, srv, token, `{"folder":"junk"}`)
		if code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		if out.Evaluated != 1 || out.Affected != 1 {
			t.Fatalf("evaluated %d affected %d, want 1 and 1 from Junk", out.Evaluated, out.Affected)
		}
		st, err := objectstore.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if fl, _ := st.MessageFlags(junk, junkMsg.UID); fl&objectstore.FlagSeen == 0 {
			t.Error("the Junk message was not touched, so the folder selection was ignored")
		}
	})

	t.Run("unknown folder", func(t *testing.T) {
		if code, _ := runFilters(t, srv, token, `{"folder":"nowhere"}`); code != http.StatusNotFound {
			t.Errorf("status %d, want 404", code)
		}
	})

	t.Run("unknown filter", func(t *testing.T) {
		if code, _ := runFilters(t, srv, token, `{"filter":"no-such-filter"}`); code != http.StatusNotFound {
			t.Errorf("status %d, want 404: a stale selection must not run everything", code)
		}
	})

	t.Run("empty body still runs the inbox", func(t *testing.T) {
		code, out := runFilters(t, srv, token, "")
		if code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		if out.Evaluated != 1 {
			t.Errorf("evaluated %d, want the Inbox's 1", out.Evaluated)
		}
	})
}
