package dav

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

const (
	testUser = "alice@hermex.test"
	testPass = "secret"
)

// seedMailbox creates a mailbox seeded with the named contacts and returns a
// StaticAccounts authorizing the test user to it.
func seedMailbox(t *testing.T, names ...string) directory.StaticAccounts {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		msg := &oxcmail.Message{Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Contact"},
			{Tag: mapi.PrDisplayName, Value: name},
		}}
		if _, err := st.CreateMessage(mapi.PrivateFIDContacts, msg); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	return directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
}

// davServer starts a test DAV server over a mailbox seeded with the given
// contacts.
func davServer(t *testing.T, names ...string) *httptest.Server {
	t.Helper()
	accs := seedMailbox(t, names...)
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

// do issues a DAV request with optional Basic auth and Depth header.
func do(t *testing.T, ts *httptest.Server, method, path, depth string, auth bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	if auth {
		req.SetBasicAuth(testUser, testPass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

// TestRequiresAuth confirms an unauthenticated request is challenged, not served.
func TestRequiresAuth(t *testing.T) {
	ts := davServer(t)
	resp, _ := do(t, ts, "PROPFIND", "/dav/addressbooks/"+testUser+"/contacts/", "1", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Errorf("missing Basic challenge: %q", resp.Header.Get("WWW-Authenticate"))
	}
}

// TestWellKnownRedirect confirms RFC 6764 autodiscovery: /.well-known/{carddav,
// caldav} redirects (301) to the DAV root so a client can bootstrap discovery,
// and that the redirect is served without authentication (no credentials sent).
func TestWellKnownRedirect(t *testing.T) {
	ts := davServer(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	for _, p := range []string{"/.well-known/carddav", "/.well-known/caldav"} {
		req, err := http.NewRequest("GET", ts.URL+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMovedPermanently {
			t.Errorf("%s: status %d, want 301", p, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/dav/" {
			t.Errorf("%s: Location %q, want /dav/", p, loc)
		}
	}
}

// TestOptions confirms OPTIONS advertises CardDAV support and the implemented
// methods.
func TestOptions(t *testing.T) {
	ts := davServer(t)
	resp, _ := do(t, ts, "OPTIONS", "/dav/addressbooks/"+testUser+"/contacts/", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if dav := resp.Header.Get("DAV"); !strings.Contains(dav, "addressbook") {
		t.Errorf("DAV header %q lacks addressbook", dav)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "PROPFIND") {
		t.Errorf("Allow %q lacks PROPFIND", allow)
	}
}

// TestPropfindAddressbook checks that a Depth 1 PROPFIND on the Contacts
// collection returns 207 with the collection and one entry per seeded contact.
func TestPropfindAddressbook(t *testing.T) {
	ts := davServer(t, "Ada Lovelace", "Grace Hopper")
	resp, body := do(t, ts, "PROPFIND", "/dav/addressbooks/"+testUser+"/contacts/", "1", true)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"multistatus", "Contacts", "getctag", ".vcf", "getetag"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
	if n := strings.Count(body, ".vcf"); n != 2 {
		t.Errorf("got %d .vcf hrefs, want 2\n%s", n, body)
	}
}

// TestPrincipalDiscovery checks the discovery chain: a principal PROPFIND
// advertises the addressbook-home-set.
func TestPrincipalDiscovery(t *testing.T) {
	ts := davServer(t)
	resp, body := do(t, ts, "PROPFIND", "/dav/principals/"+testUser+"/", "0", true)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "addressbook-home-set") || !strings.Contains(body, "/dav/addressbooks/"+testUser+"/") {
		t.Errorf("principal response lacks addressbook-home-set\n%s", body)
	}
}
