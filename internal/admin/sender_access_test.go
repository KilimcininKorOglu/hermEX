package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestSenderAccessPageRenders proves the page lists the existing allow/block rules
// for a system admin.
func TestSenderAccessPageRenders(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		senderRules: []directory.SenderRule{
			{Pattern: "friend@partner.example", Action: directory.SenderAllow},
			{Pattern: "spammy.example", Action: directory.SenderBlock},
		},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	page := wantBody(t, authedGET(t, ts, "/admin/ui/sender-access", session), http.StatusOK, "sender-access page")
	if !strings.Contains(page, "friend@partner.example") || !strings.Contains(page, "spammy.example") {
		t.Errorf("page missing the rule rows:\n%s", page)
	}
}

// TestAddSenderRule proves the add form persists a rule and acknowledges it.
func TestAddSenderRule(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPOST(t, ts, "/admin/ui/sender-access", "", csrf,
		url.Values{"pattern": {"block@evil.example"}, "action": {directory.SenderBlock}}),
		http.StatusUnauthorized, "unauth add sender rule")
	wantEq(t, len(d.senderRules), 0, "unauth request added sender rule")

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/sender-access", session, csrf,
		url.Values{"pattern": {"block@evil.example"}, "action": {directory.SenderBlock}}),
		http.StatusOK, "add sender rule")
	wantContains(t, body, "Rule saved", "add sender rule")
	if len(d.senderRules) != 1 || d.senderRules[0].Pattern != "block@evil.example" || d.senderRules[0].Action != directory.SenderBlock {
		t.Errorf("rule not persisted: %+v", d.senderRules)
	}
}

// TestSenderRulesNeedASystemAdmin proves a domain admin can neither add nor remove
// an operator sender rule: an operator block is absolute for every domain, so a
// domain admin able to allow past it would lift it for all of them.
func TestSenderRulesNeedASystemAdmin(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		senderRules: []directory.SenderRule{{Pattern: "spammy.example", Action: directory.SenderBlock}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPOST(t, ts, "/admin/ui/sender-access", session, csrf,
		url.Values{"pattern": {"spammy.example"}, "action": {directory.SenderAllow}}), http.StatusForbidden, "domain-admin add")
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/sender-access/delete", session, csrf,
		url.Values{"pattern": {"spammy.example"}}), http.StatusForbidden, "domain-admin delete")
	if len(d.senderRules) != 1 || d.senderRules[0].Action != directory.SenderBlock {
		t.Errorf("a domain admin changed the operator rules: %+v", d.senderRules)
	}
}

// TestAddSenderRuleRejectsEmptyPattern proves an empty pattern is rejected and
// nothing is stored.
func TestAddSenderRuleRejectsEmptyPattern(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/sender-access", session, csrf,
		url.Values{"pattern": {"   "}, "action": {directory.SenderAllow}}),
		http.StatusOK, "empty sender rule")
	if !strings.Contains(body, "required") {
		t.Errorf("expected a validation message:\n%s", body)
	}
	if len(d.senderRules) != 0 {
		t.Errorf("nothing should have been stored, got %+v", d.senderRules)
	}
}

// TestDeleteSenderRule proves the remove button deletes a rule.
func TestDeleteSenderRule(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		senderRules: []directory.SenderRule{{Pattern: "gone@example.com", Action: directory.SenderBlock}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPOST(t, ts, "/admin/ui/sender-access/delete", "", csrf,
		url.Values{"pattern": {"gone@example.com"}}),
		http.StatusUnauthorized, "unauth delete sender rule")
	wantEq(t, len(d.senderRules), 1, "unauth delete removed sender rule")

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/sender-access/delete", session, csrf,
		url.Values{"pattern": {"gone@example.com"}}),
		http.StatusOK, "delete sender rule")
	wantContains(t, body, "Rule removed", "delete sender rule")
	if len(d.senderRules) != 0 {
		t.Errorf("rule should have been removed, got %+v", d.senderRules)
	}
}
