package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
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
