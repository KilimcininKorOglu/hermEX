package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/tlsrpt"
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
// authenticate against this server. The values are load-bearing, a wrong MX
// target or a CNAME pointing elsewhere silently breaks delivery or autodiscovery,
// so the test pins the host the records point at, not merely that a row exists.
func TestPrescribeDomainDNS(t *testing.T) {
	const host = "mail.hermex.test"
	recs := prescribeDomainDNS("tenant.com", host, "hermex._domainkey.tenant.com", "v=DKIM1; k=rsa; p=ABC",
		directory.MTASTSSettings{Enabled: false})
	by := byLabel(recs)
	// With MTA-STS publishing off, its records must be absent, their host serves no
	// policy yet, so prescribing them would point senders at a 404.
	for _, lbl := range []string{"MTA-STS host", "MTA-STS", "TLS reporting"} {
		wantNoRecord(t, by, lbl)
	}

	// MX must carry a priority and point inbound mail at the server host.
	wantRecord(t, by, "MX", "MX", "tenant.com", "10 "+host)
	// SPF authorizes the MX host; the "mx" mechanism covers the server so no
	// separate include is needed.
	wantRecordPrefix(t, by, "SPF", "TXT", "v=spf1")
	// The DKIM row publishes exactly what the signer generated, name and value
	// verbatim, so the operator copies the real key, not a guess.
	wantRecordName(t, by, "DKIM", "hermex._domainkey.tenant.com")
	wantRecordValue(t, by, "DKIM", "v=DKIM1; k=rsa; p=ABC")
	// DMARC must target _dmarc.<domain> and declare an enforcing policy.
	wantRecordName(t, by, "DMARC", "_dmarc.tenant.com")
	wantRecordContains(t, by, "DMARC", "TXT", "v=DMARC1")
	// The mail-host CNAME points IMAP/POP3/SMTP clients at the server and, in ACME
	// mode, lets it obtain a certificate for mail.<domain>.
	wantRecord(t, by, "Mail host", "CNAME", "mail.tenant.com", host)
	// Autodiscover/Autoconfig CNAMEs must point at the server host or clients can't
	// find their settings.
	wantRecordValue(t, by, "Autodiscover", host)
	wantRecordType(t, by, "Autodiscover", "CNAME")
	wantRecordValue(t, by, "Autoconfig", host)
	wantRecordType(t, by, "Autoconfig", "CNAME")
	// The SRV fallback must advertise autodiscovery on 443 at the server host, and
	// the client-autoconfiguration SRV records must advertise the secure ports there
	// too, so clients connect over TLS, not in the clear.
	for _, c := range []struct{ label, suffix string }{
		{"Autodiscover SRV", "443 " + host},
		{"IMAP SRV", "993 " + host},
		{"POP3 SRV", "995 " + host},
		{"Submission SRV", "587 " + host},
		{"CalDAV SRV", "443 " + host},
		{"CardDAV SRV", "443 " + host},
	} {
		wantRecordSuffix(t, by, c.label, "SRV", c.suffix)
	}
	// The DAV TXT advertises the well-known DAV path.
	wantRecordType(t, by, "DAV TXT", "TXT")
	wantRecordValue(t, by, "DAV TXT", "path=/dav")
}

// wantNoRecord fails the test when the prescription carries a record it must not.
func wantNoRecord(t *testing.T, by map[string]prescribedRecord, label string) {
	t.Helper()
	if _, ok := by[label]; ok {
		t.Errorf("%q present, want absent", label)
	}
}

// wantRecordType asserts a prescribed record's DNS type.
func wantRecordType(t *testing.T, by map[string]prescribedRecord, label, typ string) {
	t.Helper()
	wantEq(t, by[label].Type, typ, label+" type")
}

// wantRecordName asserts the owner name a prescribed record is published at.
func wantRecordName(t *testing.T, by map[string]prescribedRecord, label, name string) {
	t.Helper()
	wantEq(t, by[label].Name, name, label+" name")
}

// wantRecordValue asserts a prescribed record's exact value.
func wantRecordValue(t *testing.T, by map[string]prescribedRecord, label, value string) {
	t.Helper()
	wantEq(t, by[label].Value, value, label+" value")
}

// wantRecord asserts a prescribed record's type, owner name and exact value.
func wantRecord(t *testing.T, by map[string]prescribedRecord, label, typ, name, value string) {
	t.Helper()
	wantRecordType(t, by, label, typ)
	wantRecordName(t, by, label, name)
	wantRecordValue(t, by, label, value)
}

// wantRecordPrefix asserts a record's type and that its value begins with prefix.
func wantRecordPrefix(t *testing.T, by map[string]prescribedRecord, label, typ, prefix string) {
	t.Helper()
	wantRecordType(t, by, label, typ)
	if !strings.HasPrefix(by[label].Value, prefix) {
		t.Errorf("%s value = %q, want it to begin with %q", label, by[label].Value, prefix)
	}
}

// wantRecordSuffix asserts a record's type and that its value ends with suffix.
func wantRecordSuffix(t *testing.T, by map[string]prescribedRecord, label, typ, suffix string) {
	t.Helper()
	wantRecordType(t, by, label, typ)
	if !strings.HasSuffix(by[label].Value, suffix) {
		t.Errorf("%s value = %q, want it to end with %q", label, by[label].Value, suffix)
	}
}

// wantRecordContains asserts a record's type and that its value carries sub.
func wantRecordContains(t *testing.T, by map[string]prescribedRecord, label, typ, sub string) {
	t.Helper()
	wantRecordType(t, by, label, typ)
	wantContains(t, by[label].Value, sub, label+" value")
}

// TestPrescribeDomainDNSWithoutDKIMKey proves the prescription stays complete
// before a DKIM key exists: the DKIM requirement is still listed (so the owner
// knows it is needed) but, with no key, the row points at the DKIM panel instead
// of a value, a placeholder must never read as a publishable record.
func TestPrescribeDomainDNSWithoutDKIMKey(t *testing.T) {
	recs := prescribeDomainDNS("tenant.com", "mail.hermex.test", "", "", directory.MTASTSSettings{Enabled: false})
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

// TestPrescribeDomainDNSWithMTASTS proves that enabling publishing adds exactly the
// records an MTA-STS deployment needs: the policy host CNAME (so mta-sts.<domain>
// resolves to the server and gets a certificate), the _mta-sts presence record whose
// id is a 32-char policy fingerprint, and the TLSRPT reporting record. The id must be
// a real fingerprint, not a placeholder, or senders cannot detect a policy change.
func TestPrescribeDomainDNSWithMTASTS(t *testing.T) {
	recs := prescribeDomainDNS("tenant.com", "mail.hermex.test", "hermex._domainkey.tenant.com", "v=DKIM1; p=ABC",
		directory.MTASTSSettings{Enabled: true, Mode: "testing", MaxAge: 86400})
	by := byLabel(recs)

	wantRecord(t, by, "MTA-STS host", "CNAME", "mta-sts.tenant.com", "mail.hermex.test")

	wantRecordName(t, by, "MTA-STS", "_mta-sts.tenant.com")
	wantRecordPrefix(t, by, "MTA-STS", "TXT", "v=STSv1; id=")
	id := strings.TrimPrefix(by["MTA-STS"].Value, "v=STSv1; id=")
	wantEq(t, len(id), 32, "policy id length")

	wantRecord(t, by, "TLS reporting", "TXT", "_smtp._tls.tenant.com",
		"v=TLSRPTv1; rua=mailto:postmaster@tenant.com,https://mail.hermex.test/tlsrpt")
	// The record must read back as both reporting addresses under the same parser
	// this server's own reporter uses, or a sender reading it would drop one.
	pol, err := tlsrpt.Parse(by["TLS reporting"].Value)
	if err != nil {
		t.Fatalf("the prescribed record does not parse: %v", err)
	}
	wantEq(t, strings.Join(pol.RUAs, " "), "mailto:postmaster@tenant.com https://mail.hermex.test/tlsrpt", "reporting addresses")
}

// TestDomainDetailShowsDNSRecords proves the prescription actually reaches the
// page a system admin sees, the section and records pointing at the server host
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
