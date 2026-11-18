package ews

import (
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// hidingAccounts is a directory whose GAL carries the operator's hide mask, which
// the static directory never sets. It records the hide bits each search asked the
// directory for.
type hidingAccounts struct {
	directory.StaticAccounts
	hidden map[string]uint32
	asked  *[]uint32
}

func (h hidingAccounts) SearchGAL(caller, q string, limit int, hide uint32) ([]directory.GALEntry, error) {
	*h.asked = append(*h.asked, hide)
	entries, err := h.StaticAccounts.SearchGAL(caller, q, limit, hide)
	for i := range entries {
		entries[i].HiddenFrom = h.hidden[entries[i].Address]
	}
	return entries, err
}

// hidingServer starts an EWS server whose GAL holds one visible and one hidden
// user, both matching the query prefix "al".
func hidingServer(t *testing.T, mask uint32) *httptest.Server {
	ts, _ := hidingServerAsked(t, mask)
	return ts
}

// hidingServerAsked is hidingServer that also returns the hide bits every GAL
// search asked the directory for.
func hidingServerAsked(t *testing.T, mask uint32) (*httptest.Server, *[]uint32) {
	t.Helper()
	asked := &[]uint32{}
	accs := hidingAccounts{
		StaticAccounts: directory.StaticAccounts{
			"alice@hermex.test": {Password: testPass, MailboxPath: provisionedDir(t)},
			"alex@hermex.test":  {Password: testPass, MailboxPath: provisionedDir(t)},
		},
		hidden: map[string]uint32{"alex@hermex.test": mask},
		asked:  asked,
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, asked
}

// TestGALSearchesAskTheDirectoryToHide proves each EWS surface hands the
// directory the hide bits it honors, so hidden entries are dropped before the
// directory's row limit instead of using it up: ResolveNames the GAL and name
// resolution bits, FindPeople and GetPersona the GAL bit.
func TestGALSearchesAskTheDirectoryToHide(t *testing.T) {
	persona := `<GetPersona xmlns="` + nsMessages + `"><EmailAddress xmlns="` + nsTypes +
		`"><EmailAddress>alice@hermex.test</EmailAddress></EmailAddress></GetPersona>`
	for _, tc := range []struct {
		name string
		req  string
		want uint32
	}{
		{"ResolveNames", resolveReq("al"), directory.HideFromGAL | directory.HideFromResolve},
		{"FindPeople", wrapRequest(`<FindPeople xmlns="` + nsMessages + `"><QueryString>al</QueryString></FindPeople>`), directory.HideFromGAL},
		{"GetPersona", wrapRequest(persona), directory.HideFromGAL},
		{"GetUserPhoto", getUserPhotoBody("alex@hermex.test"), directory.HideFromGAL},
	} {
		ts, asked := hidingServerAsked(t, 0)
		soapPost(t, ts, tc.req, true)
		if got := *asked; len(got) == 0 || got[0] != tc.want {
			t.Errorf("%s: hide bits asked of the directory = %v, want %d first", tc.name, got, tc.want)
		}
	}
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
