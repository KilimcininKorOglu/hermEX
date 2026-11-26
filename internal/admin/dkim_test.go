package admin

import (
	"net/http"
	"net/http/httptest"
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
// but does NOT enable signing, and the response shows the DNS record to publish, so
// signing never starts before the operator publishes the record and enables it.
func TestDKIMGenerateStoresDisabled(t *testing.T) {
	d := dkimTestDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/domains/1/dkim/generate", session, csrf, url.Values{})
	body := readBody(t, resp)
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

// TestDKIMPanelReportsAFailedKeyRead proves a failed key read is reported and the
// panel offers no key controls. It rendered as "No DKIM key for this domain" with a
// generate form, and generating replaces the stored key the published record names.
func TestDKIMPanelReportsAFailedKeyRead(t *testing.T) {
	d := dkimTestDir()
	d.dkimFound, d.dkimSelector, d.dkimPublicTXT = true, dkimSelector, "v=DKIM1; k=rsa; p=AAAA"
	d.readErrs = map[string]error{"GetDKIMKeyInfo": errReadFailed}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	page := wantBody(t, authedGET(t, ts, "/admin/ui/domains/1", session), http.StatusOK, "domain page")
	panel := wantBody(t, htmxPUT(t, ts, "/admin/ui/domains/1/dkim/enable", session, csrf, url.Values{"enabled": {"1"}}), http.StatusOK, "enable")
	_, output := dkimOutputFragment(t, ts, "/admin/ui/domains/1/dkim/output?mode=txt", session)
	for name, body := range map[string]string{"domain page": page, "enable": panel, "output": output} {
		wantContains(t, body, "DKIM key. The key controls are hidden", name+": the failed read is reported")
		if strings.Contains(body, "/dkim/generate") || strings.Contains(body, "No DKIM key for this domain") {
			t.Errorf("%s: a failed read offers to generate a key:\n%s", name, body)
		}
	}
}

// dkimOutputFragment fetches the read-only output fragment, which carries no CSRF token
// because it writes nothing.
func dkimOutputFragment(t *testing.T, ts *httptest.Server, path, session string) (int, string) {
	t.Helper()
	resp := authedGET(t, ts, path, session)
	defer resp.Body.Close()
	body := readBody(t, resp)
	return resp.StatusCode, string(body)
}

// TestDKIMGenerateStoresTheOperatorSelector proves a named selector is what gets stored,
// so the key publishes under the name the operator chose.
func TestDKIMGenerateStoresTheOperatorSelector(t *testing.T) {
	d := dkimTestDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/domains/1/dkim/generate", session, csrf, url.Values{"selector": {"S2024"}})
	resp.Body.Close()

	if d.dkimSelector != "s2024" {
		t.Errorf("selector = %q, want the lower-cased s2024", d.dkimSelector)
	}
}

// TestDKIMGenerateRejectsAnInvalidSelector is the load-bearing refusal: a selector that
// cannot be a DNS label must store nothing, or the key would publish under a name no
// receiver can look up while the panel claims a key exists.
func TestDKIMGenerateRejectsAnInvalidSelector(t *testing.T) {
	d := dkimTestDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	for _, sel := range []string{
		"bad selector!", "-lead", "trail-", "a..b", ".a", "a.", "a_b", "../etc", "s\x00x", strings.Repeat("a", 64),
	} {
		resp := htmxPOST(t, ts, "/admin/ui/domains/1/dkim/generate", session, csrf, url.Values{"selector": {sel}})
		resp.Body.Close()
		if d.dkimFound {
			t.Errorf("the invalid selector %q stored a key under %q", sel, d.dkimSelector)
			d.dkimFound = false
		}
	}
}

// TestDKIMGenerateAcceptsAValidSelector is the control for the refusals above: the
// RFC 6376 selector grammar allows hyphens inside a label, dots between labels, and
// a 63-character name.
func TestDKIMGenerateAcceptsAValidSelector(t *testing.T) {
	d := dkimTestDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	for _, sel := range []string{"s-1", "2026.mail", strings.Repeat("a", 63)} {
		d.dkimFound = false
		resp := htmxPOST(t, ts, "/admin/ui/domains/1/dkim/generate", session, csrf, url.Values{"selector": {sel}})
		resp.Body.Close()
		if !d.dkimFound || d.dkimSelector != sel {
			t.Errorf("selector %q stored found=%v as %q", sel, d.dkimFound, d.dkimSelector)
		}
	}
}

// TestDKIMGenerateRejectsAnUnknownKeyType proves an algorithm hermEX cannot generate is
// refused rather than answered with a key of a different algorithm.
func TestDKIMGenerateRejectsAnUnknownKeyType(t *testing.T) {
	d := dkimTestDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/domains/1/dkim/generate", session, csrf, url.Values{"keytype": {"dsa"}})
	resp.Body.Close()

	if d.dkimFound {
		t.Error("an unknown key type must store no key")
	}
}

// TestDKIMGenerateEd25519PublishesItsAlgorithm proves the key type reaches the generator:
// the stored record names ed25519, not the rsa default.
func TestDKIMGenerateEd25519PublishesItsAlgorithm(t *testing.T) {
	d := dkimTestDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/domains/1/dkim/generate", session, csrf, url.Values{"keytype": {"ed25519"}})
	resp.Body.Close()

	if !strings.Contains(d.dkimPublicTXT, "k=ed25519") {
		t.Errorf("stored record = %q, want a k=ed25519 tag", d.dkimPublicTXT)
	}
}

// TestDKIMOutputTXTMode proves the txt mode serves the record value on its own.
func TestDKIMOutputTXTMode(t *testing.T) {
	d := dkimTestDir()
	d.dkimFound, d.dkimSelector, d.dkimPublicTXT = true, "hermex", "v=DKIM1; k=rsa; p=AAA"
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	status, body := dkimOutputFragment(t, ts, "/admin/ui/domains/1/dkim/output?mode=txt", session)

	if status != http.StatusOK {
		t.Fatalf("output = %d, want 200", status)
	}
	if !strings.Contains(body, "v=DKIM1; k=rsa; p=AAA") {
		t.Errorf("txt mode must serve the record value:\n%s", body)
	}
}

// TestDKIMOutputKeyMode proves the key mode serves the base64 key alone, with no tags.
func TestDKIMOutputKeyMode(t *testing.T) {
	d := dkimTestDir()
	d.dkimFound, d.dkimSelector, d.dkimPublicTXT = true, "hermex", "v=DKIM1; k=rsa; p=AAA"
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	_, body := dkimOutputFragment(t, ts, "/admin/ui/domains/1/dkim/output?mode=key", session)

	if strings.Contains(body, "v=DKIM1") {
		t.Errorf("key mode must serve the key alone, without the record tags:\n%s", body)
	}
	if !strings.Contains(body, "AAA") {
		t.Errorf("key mode does not serve the key at all:\n%s", body)
	}
}

// TestDKIMOutputRecordMode proves the default mode serves a zone-file line naming the
// record.
func TestDKIMOutputRecordMode(t *testing.T) {
	d := dkimTestDir()
	d.dkimFound, d.dkimSelector, d.dkimPublicTXT = true, "hermex", "v=DKIM1; k=rsa; p=AAA"
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	_, body := dkimOutputFragment(t, ts, "/admin/ui/domains/1/dkim/output?mode=record", session)

	if !strings.Contains(body, `hermex._domainkey.hermex.test. IN TXT &#34;v=DKIM1; k=rsa; p=AAA&#34;`) {
		t.Errorf("record mode must serve a zone-file line:\n%s", body)
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

// TestDKIMNeedsASystemAdmin proves a domain administrator, even over every domain,
// can neither read, generate, toggle nor delete a DKIM key: the key signs mail as
// the domain, so replacing or deleting it breaks or hijacks the domain's signature.
func TestDKIMNeedsASystemAdmin(t *testing.T) {
	d := dkimTestDir()
	d.roles = nil
	d.perms = []directory.Permission{{Name: directory.PermDomainAdmin, Params: "*"}}
	d.dkimFound, d.dkimSelector, d.dkimPublicTXT = true, "hermex", "v=DKIM1; k=rsa; p=AAA"
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPOST(t, ts, "/admin/ui/domains/1/dkim/generate", session, csrf, url.Values{"selector": {"evil"}}), http.StatusForbidden, "generate")
	wantStatus(t, htmxPUT(t, ts, "/admin/ui/domains/1/dkim/enable", session, csrf, url.Values{"enabled": {"1"}}), http.StatusForbidden, "enable")
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/domains/1/dkim/delete", session, csrf, url.Values{}), http.StatusForbidden, "delete")
	wantStatus(t, authedGET(t, ts, "/admin/ui/domains/1/dkim/output?mode=txt", session), http.StatusForbidden, "output")
	if !d.dkimFound || d.dkimSelector != "hermex" || d.dkimEnabled {
		t.Errorf("a domain admin changed the key: found=%v selector=%q enabled=%v", d.dkimFound, d.dkimSelector, d.dkimEnabled)
	}
}

// TestDKIMGenerateNeverShowsThePrivateKey proves the generate response carries the
// public record only. The private key is stored, wrapped, in the directory and is
// exported only from the operator's shell, never over the panel.
func TestDKIMGenerateNeverShowsThePrivateKey(t *testing.T) {
	d := dkimTestDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/domains/1/dkim/generate", session, csrf, url.Values{}), http.StatusOK, "generate")
	if strings.Contains(body, "PRIVATE KEY") {
		t.Errorf("the generate response carries the private key:\n%s", body)
	}
	_, output := dkimOutputFragment(t, ts, "/admin/ui/domains/1/dkim/output?mode=record", session)
	if strings.Contains(output, "PRIVATE KEY") {
		t.Errorf("the output fragment carries the private key:\n%s", output)
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
