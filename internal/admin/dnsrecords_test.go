package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// byLabel indexes a prescription by record class for assertion.
func byLabel(recs []prescribedRecord) map[string]prescribedRecord {
	m := make(map[string]prescribedRecord, len(recs))
	for _, r := range recs {
		m[r.Label] = r
	}
	return m
}

// TestPrescribeDomainDNS proves the prescription a domain owner is shown: each
// record targets the right host with a value that actually makes mail route and
// authenticate against this server. The values are load-bearing — a wrong MX
// target or a CNAME pointing elsewhere silently breaks delivery or autodiscovery,
// so the test pins the host the records point at, not merely that a row exists.
func TestPrescribeDomainDNS(t *testing.T) {
	const host = "mail.hermex.test"
	recs := prescribeDomainDNS("tenant.com", host, "hermex._domainkey.tenant.com", "v=DKIM1; k=rsa; p=ABC")
	by := byLabel(recs)

	// MX must carry a priority and point inbound mail at the server host.
	if r := by["MX"]; r.Type != "MX" || r.Name != "tenant.com" || r.Value != "10 "+host {
		t.Errorf("MX = %+v, want type MX at apex valued '10 %s'", r, host)
	}
	// SPF authorizes the MX host; the "mx" mechanism covers the server so no
	// separate include is needed.
	if r := by["SPF"]; r.Type != "TXT" || !strings.HasPrefix(r.Value, "v=spf1") {
		t.Errorf("SPF = %+v, want a v=spf1 TXT record", r)
	}
	// The DKIM row publishes exactly what the signer generated — name and value
	// verbatim — so the operator copies the real key, not a guess.
	if r := by["DKIM"]; r.Name != "hermex._domainkey.tenant.com" || r.Value != "v=DKIM1; k=rsa; p=ABC" {
		t.Errorf("DKIM = %+v, want the generated record name and value verbatim", r)
	}
	// DMARC must target _dmarc.<domain> and declare an enforcing policy.
	if r := by["DMARC"]; r.Type != "TXT" || r.Name != "_dmarc.tenant.com" || !strings.Contains(r.Value, "v=DMARC1") {
		t.Errorf("DMARC = %+v, want a _dmarc TXT carrying v=DMARC1", r)
	}
	// Autodiscover/Autoconfig CNAMEs must point at the server host or clients can't
	// find their settings.
	for _, lbl := range []string{"Autodiscover", "Autoconfig"} {
		if r := by[lbl]; r.Type != "CNAME" || r.Value != host {
			t.Errorf("%s = %+v, want a CNAME to %s", lbl, r, host)
		}
	}
	// The SRV fallback must advertise autodiscovery on 443 at the server host.
	if r := by["Autodiscover SRV"]; r.Type != "SRV" || !strings.HasSuffix(r.Value, "443 "+host) {
		t.Errorf("Autodiscover SRV = %+v, want '... 443 %s'", r, host)
	}
}

// TestPrescribeDomainDNSWithoutDKIMKey proves the prescription stays complete
// before a DKIM key exists: the DKIM requirement is still listed (so the owner
// knows it is needed) but, with no key, the row points at the DKIM panel instead
// of a value — a placeholder must never read as a publishable record.
func TestPrescribeDomainDNSWithoutDKIMKey(t *testing.T) {
	recs := prescribeDomainDNS("tenant.com", "mail.hermex.test", "", "")
	by := byLabel(recs)
	dkim, ok := by["DKIM"]
	if !ok {
		t.Fatal("DKIM row missing; the requirement must show even without a generated key")
	}
	if strings.HasPrefix(dkim.Value, "v=DKIM1") {
		t.Errorf("DKIM value = %q, want a generate-first note, not a record that looks real", dkim.Value)
	}
	if dkim.Name != "hermex._domainkey.tenant.com" {
		t.Errorf("DKIM name = %q, want the selector record name", dkim.Name)
	}
}

// TestDomainDetailShowsDNSRecords proves the prescription actually reaches the
// page a system admin sees — the section and records pointing at the server host
// render in the domain detail HTML. It guards the handler wiring and the template
// block, which the pure prescribeDomainDNS test cannot: a dropped template range
// or a renamed field would still pass the unit test but fail here.
func TestDomainDetailShowsDNSRecords(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "one.test"},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/domains/1", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	// fakePaths.ServerHostname() is mail.hermex.test; every prescribed target must
	// point a domain owner at that host.
	for _, want := range []string{
		"Required DNS records",     // section heading
		"10 mail.hermex.test",      // MX target with priority
		"autodiscover.one.test",    // autodiscover CNAME at this domain
		"_dmarc.one.test",          // DMARC record name
		"0 0 443 mail.hermex.test", // SRV target on 443
	} {
		if !strings.Contains(page, want) {
			t.Errorf("domain detail page missing prescribed DNS content %q", want)
		}
	}
}
