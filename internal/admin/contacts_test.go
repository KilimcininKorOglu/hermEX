package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestUICreateContact proves the create form carries the address, display name
// and filing domain through to the directory and returns the refreshed panel.
func TestUICreateContact(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/contacts", session, csrf,
		url.Values{"email": {"john@partner.example"}, "displayname": {"John Partner"}, "domain": {"hermex.test"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create contact status %d, want 200", resp.StatusCode)
	}
	if d.createdContact != "john@partner.example" || d.createdContactName != "John Partner" || d.createdContactDomain != "hermex.test" {
		t.Errorf("created contact = %q name=%q domain=%q, want john@partner.example / John Partner / hermex.test",
			d.createdContact, d.createdContactName, d.createdContactDomain)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `id="contacts-panel"`) {
		t.Errorf("response is not the contacts panel fragment: %s", body)
	}
}

// TestUICreateContactRequiresDomain proves the handler rejects a missing filing
// domain with a message rather than calling the directory — a contact must ride
// on a real local domain (the domain_id foreign key).
func TestUICreateContactRequiresDomain(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/contacts", session, csrf,
		url.Values{"email": {"john@partner.example"}, "domain": {""}})
	defer resp.Body.Close()
	if d.createdContact != "" {
		t.Errorf("created a contact %q despite a missing filing domain", d.createdContact)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "filing domain are required") {
		t.Errorf("missing-domain error not reported: %s", body)
	}
}

// TestUIDeleteContact proves deletion reaches the directory and returns the
// refreshed panel.
func TestUIDeleteContact(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/contacts/john@partner.example/delete", session, csrf, url.Values{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete contact status %d, want 200", resp.StatusCode)
	}
	if d.deletedContact != "john@partner.example" {
		t.Errorf("deleted %q, want john@partner.example", d.deletedContact)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `id="contacts-panel"`) {
		t.Errorf("response is not the contacts panel fragment: %s", body)
	}
}

// TestUIContactsListRenders proves the management page renders each contact with
// its name, address and domain and offers the existing domains as filing choices
// (and catches template syntax errors).
func TestUIContactsListRenders(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		contacts: []directory.ContactInfo{{Address: "john@partner.example", DisplayName: "John Partner", Domain: "hermex.test"}},
		domains:  []directory.DomainInfo{{Name: "hermex.test"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/contacts", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("contacts page status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"John Partner", "john@partner.example", "hermex.test", `<option value="hermex.test"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("contacts page missing %q", want)
		}
	}
}
