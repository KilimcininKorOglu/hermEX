package admin

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// fakeResolver is a scripted dnsResolver: a lookup with no scripted record returns
// a not-found error, exactly as a missing record would.
type fakeResolver struct {
	mx   map[string][]*net.MX
	txt  map[string][]string
	host map[string][]string
	srv  map[string][]*net.SRV
}

func notFound() error { return &net.DNSError{Err: "no such host", IsNotFound: true} }

func (f fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if v, ok := f.mx[name]; ok {
		return v, nil
	}
	return nil, notFound()
}
func (f fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := f.txt[name]; ok {
		return v, nil
	}
	return nil, notFound()
}
func (f fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if v, ok := f.host[host]; ok {
		return v, nil
	}
	return nil, notFound()
}
func (f fakeResolver) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	if v, ok := f.srv["_"+service+"._"+proto+"."+name]; ok {
		return "", v, nil
	}
	return "", nil, notFound()
}

// acmeResolver scripts every record for acme.test except autoconfig, so a report
// over it exercises both the found and the missing paths.
func acmeResolver() fakeResolver {
	return fakeResolver{
		mx:   map[string][]*net.MX{"acme.test": {{Host: "mail.acme.test.", Pref: 10}}},
		txt:  map[string][]string{"acme.test": {"v=spf1 mx -all"}, "_dmarc.acme.test": {"v=DMARC1; p=reject"}},
		host: map[string][]string{"autodiscover.acme.test": {"1.2.3.4"}},
		srv:  map[string][]*net.SRV{"_autodiscover._tcp.acme.test": {{Target: "mail.acme.test.", Port: 443}}},
	}
}

// reportItem finds a labelled item in a report.
func reportItem(rep dnsReport, label string) (dnsCheckItem, bool) {
	for _, it := range rep.Items {
		if it.Label == label {
			return it, true
		}
	}
	return dnsCheckItem{}, false
}

// TestCheckDomainDNS proves the check reports each resolved record (with the
// trailing dot stripped) and flags a missing one rather than erroring.
func TestCheckDomainDNS(t *testing.T) {
	rep := checkDomainDNS(context.Background(), acmeResolver(), "acme.test")

	want := map[string]struct {
		ok     bool
		detail string
	}{
		"MX":               {true, "mail.acme.test"},
		"SPF":              {true, "v=spf1"},
		"DMARC":            {true, "v=DMARC1"},
		"Autodiscover":     {true, "1.2.3.4"},
		"Autodiscover SRV": {true, "mail.acme.test:443"},
		"Autoconfig":       {false, "does not resolve"},
	}
	for label, w := range want {
		it, ok := reportItem(rep, label)
		if !ok {
			t.Errorf("report missing item %q", label)
			continue
		}
		if it.OK != w.ok {
			t.Errorf("%s OK = %v, want %v (detail %q)", label, it.OK, w.ok, it.Detail)
		}
		if !strings.Contains(it.Detail, w.detail) {
			t.Errorf("%s detail = %q, want it to contain %q", label, it.Detail, w.detail)
		}
	}
}

// TestCheckDomainDNSAllMissing proves a domain with no records reports every item
// as missing rather than failing.
func TestCheckDomainDNSAllMissing(t *testing.T) {
	rep := checkDomainDNS(context.Background(), fakeResolver{}, "ghost.test")
	if len(rep.Items) == 0 {
		t.Fatal("empty report")
	}
	for _, it := range rep.Items {
		if it.OK {
			t.Errorf("%s reported OK for a domain with no records", it.Label)
		}
	}
}

// TestAdminDomainDNSCheck proves the JSON endpoint resolves the route's domain id
// to its name and returns the report.
func TestAdminDomainDNSCheck(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
	}
	ts := adminServerDNS(t, d, acmeResolver())
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/domains/1/dnscheck", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dns check status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "mail.acme.test") || !strings.Contains(string(body), "DMARC") {
		t.Errorf("dns check body = %s, want the resolved records", body)
	}
}

// TestUIDomainDNSCheck proves the UI endpoint renders the report partial.
func TestUIDomainDNSCheck(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
	}
	ts := adminServerDNS(t, d, acmeResolver())
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/domains/1/dnscheck", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui dns check status %d, want 200", resp.StatusCode)
	}
	s := string(body)
	if !strings.Contains(s, "MX") || !strings.Contains(s, "mail.acme.test") {
		t.Errorf("ui dns report missing content: %s", s)
	}
}
