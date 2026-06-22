package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

func dkimTestDir() *fakeDir {
	return &fakeDir{
		authOK:       true,
		uid:          7,
		roles:        []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "hermex.test"},
	}
}

// TestDKIMGenerateStoresDisabled is the load-bearing test: generating a key stores it
// but does NOT enable signing, and the response shows the DNS record to publish — so
// signing never starts before the operator publishes the record and enables it.
func TestDKIMGenerateStoresDisabled(t *testing.T) {
	d := dkimTestDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/domains/1/dkim/generate", session, csrf, url.Values{})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("generate = %d, want 200", resp.StatusCode)
	}
	if !d.dkimFound {
		t.Fatal("a key must be stored after generate")
	}
	if d.dkimEnabled {
		t.Error("generating a key must NOT enable signing")
	}
	if d.dkimSelector != dkimSelector {
		t.Errorf("selector = %q, want %q", d.dkimSelector, dkimSelector)
	}
	if len(d.dkimPrivPEM) == 0 || !strings.Contains(d.dkimPublicTXT, "v=DKIM1") {
		t.Errorf("stored key incomplete: priv %d bytes, txt %q", len(d.dkimPrivPEM), d.dkimPublicTXT)
	}
	if !strings.Contains(string(body), "Publish the DNS record") {
		t.Errorf("response should tell the operator to publish the record:\n%s", body)
	}
}

// TestDKIMEnableThenDisable proves enabling and disabling flip the stored flag.
func TestDKIMEnableThenDisable(t *testing.T) {
	d := dkimTestDir()
	d.dkimFound, d.dkimSelector, d.dkimPublicTXT = true, "hermex", "v=DKIM1; k=rsa; p=AAA"
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/domains/1/dkim/enable", session, csrf, url.Values{"enabled": {"1"}})
	resp.Body.Close()
	if !d.dkimEnabled {
		t.Fatal("enable must turn signing on")
	}
	resp = htmxPUT(t, ts, "/admin/ui/domains/1/dkim/enable", session, csrf, url.Values{"enabled": {"0"}})
	resp.Body.Close()
	if d.dkimEnabled {
		t.Error("disable must turn signing off")
	}
}

// TestDKIMDelete proves deleting removes the key.
func TestDKIMDelete(t *testing.T) {
	d := dkimTestDir()
	d.dkimFound = true
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/domains/1/dkim/delete", session, csrf, url.Values{})
	resp.Body.Close()
	if d.dkimFound {
		t.Error("delete must remove the key")
	}
}
