package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// ruleStoreAuth is a directory stub recording the recipient-rule calls the
// handlers make, so the API wiring can be tested without a database (the
// directory's own methods are covered in internal/directory).
type ruleStoreAuth struct {
	rules   []directory.RecipientRule
	setArgs [][3]string // user, pattern, action
	deleted []string
}

func (a *ruleStoreAuth) Authenticate(string, string) (string, bool) { return "/tmp", true }

func (a *ruleStoreAuth) ListRecipientRules(string) ([]directory.RecipientRule, error) {
	return a.rules, nil
}

func (a *ruleStoreAuth) SetRecipientRule(user, pattern, action string) error {
	a.setArgs = append(a.setArgs, [3]string{user, pattern, action})
	return nil
}

func (a *ruleStoreAuth) DeleteRecipientRule(_ string, pattern string) (bool, error) {
	a.deleted = append(a.deleted, pattern)
	return true, nil
}

// TestRecipientRulesAPI proves the GET/POST/DELETE endpoints list rules, persist a
// valid allow/block rule for the session user, reject an unknown action, and
// delete by pattern.
func TestRecipientRulesAPI(t *testing.T) {
	auth := &ruleStoreAuth{rules: []directory.RecipientRule{{Pattern: "spam@x.test", Action: directory.SenderBlock}}}
	secret := []byte("recipient-rules-secret")
	srv := NewServer(auth, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: t.TempDir(), Exp: time.Now().Add(time.Hour).Unix()})
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// GET lists the existing rules.
	type listing struct {
		Rules []recipientRuleJSON `json:"rules"`
	}
	got := okBody[listing](t, "get rules", do(http.MethodGet, "/api/v1/recipient-rules", ""))
	if len(got.Rules) != 1 {
		t.Fatalf("get rules = %+v, want one block rule for spam@x.test", got.Rules)
	}
	wantEq(t, "listed rule pattern", got.Rules[0].Pattern, "spam@x.test")
	wantEq(t, "listed rule action", got.Rules[0].Action, "block")

	// POST persists a valid rule for the session user.
	wantStatus(t, "post rule",
		do(http.MethodPost, "/api/v1/recipient-rules", `{"pattern":"friend@x.test","action":"allow"}`), http.StatusOK)
	if len(auth.setArgs) != 1 || auth.setArgs[0] != [3]string{"alice@hermex.test", "friend@x.test", "allow"} {
		t.Errorf("SetRecipientRule args = %v, want alice/friend@x.test/allow", auth.setArgs)
	}

	// POST with an unknown action is rejected before touching the store.
	wantStatus(t, "post bad action",
		do(http.MethodPost, "/api/v1/recipient-rules", `{"pattern":"x@x.test","action":"maybe"}`), http.StatusBadRequest)
	wantEq(t, "store writes after a rejected action", len(auth.setArgs), 1)

	// DELETE removes by pattern.
	wantStatus(t, "delete rule",
		do(http.MethodDelete, "/api/v1/recipient-rules?pattern=spam@x.test", ""), http.StatusOK)
	if len(auth.deleted) != 1 || auth.deleted[0] != "spam@x.test" {
		t.Errorf("DeleteRecipientRule pattern = %v, want [spam@x.test]", auth.deleted)
	}
}
