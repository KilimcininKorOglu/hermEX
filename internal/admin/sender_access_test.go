package admin

import (
	"io"
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

	resp := authedGET(t, ts, "/admin/ui/sender-access", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	if !strings.Contains(page, "friend@partner.example") || !strings.Contains(page, "spammy.example") {
		t.Errorf("page missing the rule rows:\n%s", page)
	}
}

// TestAddSenderRule proves the add form persists a rule and acknowledges it.
func TestAddSenderRule(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/sender-access", session, csrf,
		url.Values{"pattern": {"block@evil.example"}, "action": {directory.SenderBlock}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Rule saved") {
		t.Fatalf("add = %d body=%q, want 200 with ack", resp.StatusCode, body)
	}
	if len(d.senderRules) != 1 || d.senderRules[0].Pattern != "block@evil.example" || d.senderRules[0].Action != directory.SenderBlock {
		t.Errorf("rule not persisted: %+v", d.senderRules)
	}
}

// TestAddSenderRuleRejectsEmptyPattern proves an empty pattern is rejected and
// nothing is stored.
func TestAddSenderRuleRejectsEmptyPattern(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/sender-access", session, csrf,
		url.Values{"pattern": {"   "}, "action": {directory.SenderAllow}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "required") {
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

	resp := htmxPOST(t, ts, "/admin/ui/sender-access/delete", session, csrf,
		url.Values{"pattern": {"gone@example.com"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Rule removed") {
		t.Fatalf("delete = %d body=%q, want 200 with ack", resp.StatusCode, body)
	}
	if len(d.senderRules) != 0 {
		t.Errorf("rule should have been removed, got %+v", d.senderRules)
	}
}
