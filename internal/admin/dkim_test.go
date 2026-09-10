package admin

import (
	"io"
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

// dkimOutputFragment fetches the read-only output fragment, which carries no CSRF token
// because it writes nothing.
func dkimOutputFragment(t *testing.T, ts *httptest.Server, path, session string) (int, string) {
	t.Helper()
	resp := authedGET(t, ts, path, session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
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

	resp := htmxPOST(t, ts, "/admin/ui/domains/1/dkim/generate", session, csrf, url.Values{"selector": {"bad selector!"}})
	resp.Body.Close()

	if d.dkimFound {
		t.Error("an invalid selector must store no key")
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
