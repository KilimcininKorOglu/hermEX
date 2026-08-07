package ews

import (
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// hidingAccounts is a directory whose GAL carries the operator's hide mask, which
// the static directory never sets.
type hidingAccounts struct {
	directory.StaticAccounts
	hidden map[string]uint32
}

func (h hidingAccounts) SearchGAL(caller, q string, limit int) ([]directory.GALEntry, error) {
	entries, err := h.StaticAccounts.SearchGAL(caller, q, limit)
	for i := range entries {
		entries[i].HiddenFrom = h.hidden[entries[i].Address]
	}
	return entries, err
}

// hidingServer starts an EWS server whose GAL holds one visible and one hidden
// user, both matching the query prefix "al".
func hidingServer(t *testing.T, mask uint32) *httptest.Server {
	t.Helper()
	accs := hidingAccounts{
		StaticAccounts: directory.StaticAccounts{
			"alice@hermex.test": {Password: testPass, MailboxPath: t.TempDir()},
			"alex@hermex.test":  {Password: testPass, MailboxPath: t.TempDir()},
		},
		hidden: map[string]uint32{"alex@hermex.test": mask},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestResolveNamesOmitsHiddenUser proves ResolveNames withholds a user the
// operator hid from name resolution, and still resolves the visible one. With the
// hidden entry gone the query has a single match, so the outcome is Success.
func TestResolveNamesOmitsHiddenUser(t *testing.T) {
	ts := hidingServer(t, directory.HideFromResolve)

	_, body := soapPost(t, ts, resolveReq("al"), true)
	if strings.Contains(body, "alex@hermex.test") {
		t.Error("ResolveNames returned a user hidden from name resolution")
	}
	if !strings.Contains(body, "alice@hermex.test") {
		t.Errorf("ResolveNames dropped the visible user: %s", body)
	}
}

// TestResolveNamesHiddenOnlyMatchReportsNoResults proves a query that matches
// nothing but hidden entries answers exactly as an unknown name does, so the
// response is not an existence oracle for a hidden mailbox.
func TestResolveNamesHiddenOnlyMatchReportsNoResults(t *testing.T) {
	ts := hidingServer(t, directory.HideFromResolve)

	_, body := soapPost(t, ts, resolveReq("alex"), true)
	if !strings.Contains(body, "ErrorNameResolutionNoResults") {
		t.Errorf("resolving only a hidden user should report no results: %s", body)
	}
}

// TestFindPeopleOmitsHiddenUser proves the persona search follows the address
// book too.
func TestFindPeopleOmitsHiddenUser(t *testing.T) {
	ts := hidingServer(t, directory.HideFromGAL)

	req := wrapRequest(`<FindPeople xmlns="` + nsMessages + `"><QueryString>al</QueryString></FindPeople>`)
	_, body := soapPost(t, ts, req, true)
	if strings.Contains(body, "alex@hermex.test") {
		t.Error("FindPeople returned a user hidden from the address book")
	}
	if !strings.Contains(body, "alice@hermex.test") {
		t.Errorf("FindPeople dropped the visible user: %s", body)
	}
}

// TestGetPersonaOmitsHiddenUser proves a direct persona lookup of a hidden
// address answers ErrorPersonNotFound, the same as an address the directory does
// not hold.
func TestGetPersonaOmitsHiddenUser(t *testing.T) {
	ts := hidingServer(t, directory.HideFromGAL)

	req := wrapRequest(`<GetPersona xmlns="` + nsMessages + `"><EmailAddress xmlns="` + nsTypes +
		`"><EmailAddress>alex@hermex.test</EmailAddress></EmailAddress></GetPersona>`)
	_, body := soapPost(t, ts, req, true)
	if !strings.Contains(body, "ErrorPersonNotFound") {
		t.Errorf("GetPersona on a hidden address should report person not found: %s", body)
	}
}

// TestGetPersonaFindsVisibleUser is the control for the hidden-persona test: the
// same request shape against a visible address does return a persona, so the
// not-found above is the hide mask and not a malformed request.
func TestGetPersonaFindsVisibleUser(t *testing.T) {
	ts := hidingServer(t, directory.HideFromGAL)

	req := wrapRequest(`<GetPersona xmlns="` + nsMessages + `"><EmailAddress xmlns="` + nsTypes +
		`"><EmailAddress>alice@hermex.test</EmailAddress></EmailAddress></GetPersona>`)
	_, body := soapPost(t, ts, req, true)
	if strings.Contains(body, "ErrorPersonNotFound") {
		t.Errorf("GetPersona on a visible address reported not found: %s", body)
	}
	if !strings.Contains(body, "alice@hermex.test") {
		t.Errorf("GetPersona did not return the visible persona: %s", body)
	}
}
