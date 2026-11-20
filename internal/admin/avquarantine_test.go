package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// TestUIAVQuarantine proves the quarantine page renders the held messages a system
// administrator may read, with each record's sender, signature, and subject shown.
func TestUIAVQuarantine(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		quarantine: []directory.QuarantineRecord{
			{ID: 1, Status: "held", QuarantineEntry: directory.QuarantineEntry{
				Direction:  "inbound",
				MailFrom:   "attacker@evil.test",
				Recipients: []string{"alice@hermex.test"},
				Subject:    "invoice",
				VirusName:  "Eicar-Test-Signature",
				DomainID:   1,
				CreatedAt:  1700000000,
			}},
		},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/avquarantine", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{"attacker@evil.test", "Eicar-Test-Signature", "invoice", "inbound", "alice@hermex.test"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("quarantine page missing %q:\n%s", want, body)
		}
	}
}

// TestUIAVQuarantineEscapesTheSendersText proves the sender-chosen subject and
// envelope sender render as text: a held message is attacker mail by definition,
// and the page is read in the most privileged session on the system.
func TestUIAVQuarantineEscapesTheSendersText(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		quarantine: []directory.QuarantineRecord{{ID: 1, Status: "held", QuarantineEntry: directory.QuarantineEntry{
			MailFrom: `<img src=x onerror=alert(1)>@evil.test`, Subject: `<script>alert(2)</script>`, DomainID: 1,
		}}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/avquarantine", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, raw := range []string{"<script>alert(2)", "<img src=x"} {
		if strings.Contains(string(body), raw) {
			t.Errorf("the page renders %q unescaped", raw)
		}
	}
	if !strings.Contains(string(body), "&lt;script&gt;alert(2)") {
		t.Error("the subject is not shown at all, so the escaping above proves nothing")
	}
}

// TestUIAVQuarantineAsksOnlyForTheAdminsDomains proves a domain administrator's
// page lists only its own domains' held messages: another domain's quarantine
// holds that domain's users' mail.
func TestUIAVQuarantineAsksOnlyForTheAdminsDomains(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, perms: []directory.Permission{{Name: directory.PermDomainAdmin, Params: "1"}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/avquarantine", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if d.quarantineAll || len(d.quarantineIDs) != 1 || d.quarantineIDs[0] != 1 {
		t.Errorf("listed with all=%v ids=%v, want only domain 1", d.quarantineAll, d.quarantineIDs)
	}
}

// TestUIAVQuarantineEmpty proves the page shows its empty state when nothing is
// held, rather than an error.
func TestUIAVQuarantineEmpty(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/avquarantine", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "No messages are held") {
		t.Errorf("empty-state not shown:\n%s", body)
	}
}

// TestUIDomainAVScanSave proves the domain-detail antivirus form carries the
// inbound and outbound toggles to the directory: a checked box persists true, an
// omitted (unchecked) box persists false, and the save is acknowledged.
func TestUIDomainAVScanSave(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPUT(t, ts, "/admin/ui/domains/1/avscan", session, csrf,
		url.Values{"av_scan_inbound": {"on"}}) // outbound omitted: unchecked
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui domain avscan save status %d, want 200", resp.StatusCode)
	}
	if !d.avInbound || d.avOutbound {
		t.Errorf("save captured (in=%v, out=%v), want (true, false)", d.avInbound, d.avOutbound)
	}
	if !strings.Contains(string(body), `class="ok"`) {
		t.Errorf("save response = %s, want a success acknowledgement", body)
	}
}

// TestUIDomainSettingsNeedASystemAdmin proves the panel's domain antivirus,
// catch-all and split-delivery saves take a full system administrator, as every
// panel write does (uiAuthorized): a domain administrator is refused for another
// domain and for its own, since turning scanning off or pointing the catch-all or
// the relay host elsewhere exposes a domain's mail. A system administrator is the
// control: the same save is admitted, so the refusals come from the authority.
func TestUIDomainSettingsNeedASystemAdmin(t *testing.T) {
	form := url.Values{"av_scan_inbound": {"on"}, "catchall": {"x@acme.test"}, "split_relay_host": {"relay.acme.test"}}
	for _, setting := range []string{"avscan", "catchall", "split"} {
		for _, id := range []string{"1", "2"} {
			d := &fakeDir{
				authOK: true, uid: 7, perms: []directory.Permission{{Name: directory.PermDomainAdmin, Params: "1"}},
				domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
			}
			ts := adminServer(t, d)
			session, csrf := loginCookies(t, ts)
			wantStatus(t, htmxPUT(t, ts, "/admin/ui/domains/"+id+"/"+setting, session, csrf, form), http.StatusForbidden,
				"a domain admin of domain 1 saving domain "+id+"'s "+setting)
			wantFalse(t, d.avInbound, "inbound scanning after the refused "+setting+" save")
		}
		sys := &fakeDir{
			authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
		}
		ts := adminServer(t, sys)
		session, csrf := loginCookies(t, ts)
		wantStatus(t, htmxPUT(t, ts, "/admin/ui/domains/1/"+setting, session, csrf, form), http.StatusOK,
			"a system admin saving domain 1's "+setting)
	}
}

// TestUIDomainAVScanEmitsAudit proves changing a domain's antivirus toggles records
// a setting.change audit event naming the operator and the before/after values, so
// an incident responder can answer who turned AV scanning off and when.
func TestUIDomainAVScanEmitsAudit(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
		avInbound:    true, avOutbound: true, // prior state: both on
	}
	ts, sink := loggingAdminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPUT(t, ts, "/admin/ui/domains/1/avscan", session, csrf,
		url.Values{"av_scan_inbound": {"on"}}) // outbound omitted: turned off
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("avscan save status %d, want 200", resp.StatusCode)
	}

	e, ok := sink.find("setting.change")
	if !ok {
		t.Fatal("no setting.change audit event was recorded for the AV toggle change")
	}
	if e.User != "admin@hermex.test" {
		t.Errorf("audit actor = %q, want the operator login", e.User)
	}
	if e.Fields["setting"] != "av_scan" {
		t.Errorf("audit setting = %v, want av_scan", e.Fields["setting"])
	}
	if e.Fields["old_outbound"] != true || e.Fields["new_outbound"] != false {
		t.Errorf("audit did not capture the outbound before/after: %v", e.Fields)
	}
	if e.Level != logging.LevelInfo {
		t.Errorf("audit level = %v, want info (a positive event, not a failure)", e.Level)
	}
}
