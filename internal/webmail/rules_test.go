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

// TestRulesAddBodyAndSensitivity adds rules using the newer body-contains and
// sensitivity-is conditions and checks each is stored and summarized, so the
// editor's added vocabulary round-trips through save and listing.
func TestRulesAddBodyAndSensitivity(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code, _ := postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"add"}, "name": {"BodyRule"},
		"condfield": {"body"}, "condvalue": {"wire transfer"},
		"actiontype": {"markread"},
	}); code != 200 && code != 303 {
		t.Fatalf("add body rule = %d", code)
	}
	if code, _ := postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"add"}, "name": {"PrivacyRule"},
		"condfield": {"sensitivity"}, "condsensitivity": {"private"},
		"actiontype": {"markread"},
	}); code != 200 && code != 303 {
		t.Fatalf("add sensitivity rule = %d", code)
	}

	_, body := get(t, c, ts.URL+"/rules")
	// "the body contains" / "the sensitivity is private" are produced only by the
	// rule summary, never by the form's option labels, so they are discriminating.
	for _, want := range []string{"the body contains", "the sensitivity is private"} {
		if !strings.Contains(body, want) {
			t.Errorf("rules page missing %q:\n%s", want, body)
		}
	}
}

// TestRulesAddCopyAction adds a copy rule through the editor and checks it is
// listed with the copy-action summary naming the target folder.
func TestRulesAddCopyAction(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	junk := strconv.FormatInt(int64(mapi.PrivateFIDJunk), 10)
	if code, _ := postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"add"}, "name": {"CopyReports"},
		"condfield": {"subject"}, "condvalue": {"report"},
		"actiontype": {"copy"}, "actiontarget": {junk},
	}); code != 200 && code != 303 {
		t.Fatalf("add copy rule = %d", code)
	}
	_, body := get(t, c, ts.URL+"/rules")
	// "copy it to Junk Email" is produced only by a stored copy rule's summary.
	if !strings.Contains(body, "copy it to Junk Email") {
		t.Errorf("rules page missing the copy-action summary:\n%s", body)
	}
}

// ruleIDByName returns the inbox rule with the given name, failing if none.
func ruleIDByName(t *testing.T, path, name string) int64 {
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
	for _, r := range rules {
		if r.Name == name {
			return r.ID
		}
	}
	t.Fatalf("no rule named %s", name)
	return 0
}

// TestRulesReorder adds two rules, moves the second up, and checks the listing
// order flips — the move-up/down controls change which rule evaluates first.
func TestRulesReorder(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	junk := strconv.FormatInt(int64(mapi.PrivateFIDJunk), 10)

	// Add "AlphaRule" then "BetaRule"; Alpha gets the lower sequence (lists first).
	for _, name := range []string{"AlphaRule", "BetaRule"} {
		postForm(t, c, ts.URL+"/rules", url.Values{
			"action": {"add"}, "name": {name},
			"condfield": {"subject"}, "condvalue": {"x"},
			"actiontype": {"move"}, "actiontarget": {junk},
		})
	}
	_, before := get(t, c, ts.URL+"/rules")
	if strings.Index(before, "AlphaRule") > strings.Index(before, "BetaRule") {
		t.Fatalf("expected AlphaRule listed before BetaRule initially:\n%s", before)
	}

	postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"moveup"}, "id": {strconv.FormatInt(ruleIDByName(t, path, "BetaRule"), 10)},
	})
	_, after := get(t, c, ts.URL+"/rules")
	if strings.Index(after, "BetaRule") > strings.Index(after, "AlphaRule") {
		t.Errorf("after move-up BetaRule should list before AlphaRule:\n%s", after)
	}
}

// TestRulesAddCompoundCondition adds a rule whose two conditions are combined by
// AND and another by OR, checking each round-trips to the expected summary.
func TestRulesAddCompoundCondition(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	junk := strconv.FormatInt(int64(mapi.PrivateFIDJunk), 10)

	postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"add"}, "name": {"AndRule"},
		"condfield": {"subject"}, "condvalue": {"invoice"},
		"condfield2": {"from"}, "condvalue2": {"acme"}, "match": {"all"},
		"actiontype": {"move"}, "actiontarget": {junk},
	})
	postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"add"}, "name": {"OrRule"},
		"condfield": {"subject"}, "condvalue": {"urgent"},
		"condfield2": {"body"}, "condvalue2": {"asap"}, "match": {"any"},
		"actiontype": {"markread"},
	})

	_, body := get(t, c, ts.URL+"/rules")
	// "and the sender contains" appears only for the AND rule, "or the body
	// contains" only for the OR rule, so both are discriminating.
	for _, want := range []string{"and the sender contains", "or the body contains"} {
		if !strings.Contains(body, want) {
			t.Errorf("rules page missing %q:\n%s", want, body)
		}
	}
}

// TestRulesAddStopProcessing adds a rule with the stop-processing option and
// checks the listing notes it, so the exit-level bit round-trips through the form.
func TestRulesAddStopProcessing(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	if code, _ := postForm(t, c, ts.URL+"/rules", url.Values{
		"action": {"add"}, "name": {"StopRule"},
		"condfield": {"subject"}, "condvalue": {"urgent"},
		"actiontype": {"markread"}, "stop": {"on"},
	}); code != 200 && code != 303 {
		t.Fatalf("add stop rule = %d", code)
	}
	_, body := get(t, c, ts.URL+"/rules")
	if !strings.Contains(body, "stop processing more rules") {
		t.Errorf("rules page missing the stop-processing note:\n%s", body)
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
