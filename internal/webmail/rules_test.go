package webmail

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// firstRuleID returns the id of the inbox's first rule, failing if none exist.
func firstRuleID(t *testing.T, path string) int64 {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rules, err := st.ListRules(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) == 0 {
		t.Fatal("no rules stored")
	}
	return rules[0].ID
}

// TestRulesAddListDelete adds a move rule through the editor form, checks it is
// listed with a readable summary naming the target folder, then deletes it.
func TestRulesAddListDelete(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// A distinctive name that does not appear in any form placeholder, so the
	// listing checks below are not fooled by static template text.
	const ruleName = "AcmeBillingRule"
	junk := strconv.FormatInt(int64(mapi.PrivateFIDJunk), 10)
	if code, _ := postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"add"}, "name": {ruleName},
		"condfield": {"subject"}, "condvalue": {"invoice"},
		"actiontype": {"move"}, "actiontarget": {junk},
	}); code != 200 && code != 303 {
		t.Fatalf("add = %d", code)
	}

	_, body := get(t, c, ts.URL+"/rules")
	// "move it to Junk Email" is rendered only for a stored move rule (never in a
	// placeholder), so it is the discriminating assertion.
	for _, want := range []string{ruleName, "the subject contains", "move it to Junk Email"} {
		if !strings.Contains(body, want) {
			t.Errorf("rules page missing %q:\n%s", want, body)
		}
	}

	id := firstRuleID(t, path)
	if code, _ := postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"delete"}, "id": {strconv.FormatInt(id, 10)},
	}); code != 200 && code != 303 {
		t.Fatalf("delete = %d", code)
	}
	_, body2 := get(t, c, ts.URL+"/rules")
	if strings.Contains(body2, ruleName) || strings.Contains(body2, "move it to Junk Email") {
		t.Errorf("rule still listed after delete:\n%s", body2)
	}
}

// TestRulesToggle disables and re-enables a rule, checking the listing reflects
// the state each time.
func TestRulesToggle(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"add"}, "condfield": {"subject"}, "condvalue": {"news"},
		"actiontype": {"markread"},
	})
	_, body := get(t, c, ts.URL+"/rules")
	if strings.Contains(body, "(disabled)") {
		t.Fatalf("a freshly added rule should be enabled:\n%s", body)
	}

	id := firstRuleID(t, path)
	postForm(t, c, ts.URL+"/rules", url.Values{"action": {"toggle"}, "id": {strconv.FormatInt(id, 10)}, "enabled": {"0"}})
	_, off := get(t, c, ts.URL+"/rules")
	if !strings.Contains(off, "(disabled)") {
		t.Errorf("rule not shown disabled after toggle off:\n%s", off)
	}

	postForm(t, c, ts.URL+"/rules", url.Values{"action": {"toggle"}, "id": {strconv.FormatInt(id, 10)}, "enabled": {"1"}})
	_, on := get(t, c, ts.URL+"/rules")
	if strings.Contains(on, "(disabled)") {
		t.Errorf("rule still shown disabled after toggle on:\n%s", on)
	}
}

// TestRulesRunNow seeds an unread inbox message, adds a mark-read rule matching
// it, runs the rules on demand, and checks both the result notice and that the
// message is now read.
func TestRulesRunNow(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "your invoice is ready", "alice@hermex.test", "body", 1700000000, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"add"}, "condfield": {"subject"}, "condvalue": {"invoice"},
		"actiontype": {"markread"},
	})

	_, body := postForm(t, c, ts.URL+"/rules", url.Values{"action": {"run"}})
	if !strings.Contains(body, "message(s) affected") {
		t.Errorf("run-now result notice missing:\n%s", body)
	}

	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, err := st.MessageByUID(int64(mapi.PrivateFIDInbox), uid)
	if err != nil {
		t.Fatal(err)
	}
	if m.Flags&objectstore.FlagSeen == 0 {
		t.Errorf("run-now did not mark the matching message read")
	}
}

// TestRulesAddIncomplete checks that submitting the add form with an empty
// condition value reports an error and stores no rule.
func TestRulesAddIncomplete(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, body := postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"add"}, "condfield": {"subject"}, "condvalue": {"  "},
		"actiontype": {"markread"},
	})
	if !strings.Contains(body, "Could not add the rule") {
		t.Errorf("expected an error notice for the incomplete condition:\n%s", body)
	}
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rules, _ := st.ListRules(int64(mapi.PrivateFIDInbox))
	if len(rules) != 0 {
		t.Errorf("an incomplete rule was stored: %d rules", len(rules))
	}
}
