package webmail

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// fakeRuleStore is an in-memory RecipientRuleStore that records the username the
// handler resolved, so a test can prove a rule is filed for the logged-in user.
type fakeRuleStore struct {
	rules    map[string]string // pattern -> action
	lastUser string
}

func (f *fakeRuleStore) ListRecipientRules(username string) ([]directory.RecipientRule, error) {
	var out []directory.RecipientRule
	for p, a := range f.rules {
		out = append(out, directory.RecipientRule{Pattern: p, Action: a})
	}
	return out, nil
}

func (f *fakeRuleStore) SetRecipientRule(username, pattern, action string) error {
	f.lastUser = username
	if f.rules == nil {
		f.rules = map[string]string{}
	}
	f.rules[strings.ToLower(strings.TrimSpace(pattern))] = action
	return nil
}

func (f *fakeRuleStore) DeleteRecipientRule(username, pattern string) (bool, error) {
	f.lastUser = username
	key := strings.ToLower(strings.TrimSpace(pattern))
	if _, ok := f.rules[key]; ok {
		delete(f.rules, key)
		return true, nil
	}
	return false, nil
}

// newRuleServer is newTestServer with a RecipientRuleStore wired in.
func newRuleServer(t *testing.T, path string, store RecipientRuleStore) *httptest.Server {
	t.Helper()
	auth := directory.StaticAccounts{"alice@hermex.test": {Password: "secret", MailboxPath: path}}
	srv, err := NewServer(auth, auth, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	srv.Rules = store
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getBody(t *testing.T, c *http.Client, u string) string {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestSettingsAddAndDeleteRule proves the settings page files a rule for the
// logged-in user and that the page lists it back, then removes it on delete — the
// whole user-facing loop for managing personal allow/block rules.
func TestSettingsAddAndDeleteRule(t *testing.T) {
	store := &fakeRuleStore{}
	ts := newRuleServer(t, emptyMailbox(t), store)
	c := authedClient(t, ts)

	if code, body := postForm(t, c, ts.URL+"/settings", url.Values{
		"action": {"addrule"}, "pattern": {"boss@example.com"}, "ruleaction": {"allow"},
	}); code != 200 {
		t.Fatalf("addrule status = %d, want 200 after redirect; body=%s", code, body)
	}
	if store.lastUser != "alice@hermex.test" {
		t.Errorf("rule filed for %q, want the logged-in user alice@hermex.test", store.lastUser)
	}
	if store.rules["boss@example.com"] != "allow" {
		t.Errorf("store = %v, want boss@example.com -> allow", store.rules)
	}
	if body := getBody(t, c, ts.URL+"/settings"); !strings.Contains(body, "boss@example.com") {
		t.Errorf("settings page does not list the new rule:\n%s", body)
	}

	if _, _ = postForm(t, c, ts.URL+"/settings", url.Values{
		"action": {"delrule"}, "pattern": {"boss@example.com"},
	}); len(store.rules) != 0 {
		t.Errorf("store = %v, want empty after delete", store.rules)
	}
}

// TestSettingsRuleSectionHiddenWithoutStore proves the allow/block section is absent
// when no rule store is wired, so the page never offers a control that cannot work.
func TestSettingsRuleSectionHiddenWithoutStore(t *testing.T) {
	ts := newTestServer(t, emptyMailbox(t)) // no Rules wired
	c := authedClient(t, ts)
	if body := getBody(t, c, ts.URL+"/settings"); strings.Contains(body, "Allow / block senders") {
		t.Errorf("allow/block section shown without a rule store:\n%s", body)
	}
}
