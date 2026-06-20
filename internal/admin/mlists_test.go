package admin

import (
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestUICreateMList proves the create form carries the address, type and
// privilege through to the directory and returns the refreshed panel.
func TestUICreateMList(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/mlists", session, csrf,
		url.Values{"listname": {"team@hermex.test"}, "type": {"2"}, "privilege": {"3"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create list status %d, want 200", resp.StatusCode)
	}
	if d.createdMList != "team@hermex.test" || d.createdMListType != 2 || d.createdMListPriv != 3 {
		t.Errorf("created list = %q type=%d priv=%d, want team@hermex.test type=2 priv=3",
			d.createdMList, d.createdMListType, d.createdMListPriv)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `id="mlists-panel"`) {
		t.Errorf("response is not the lists panel fragment: %s", body)
	}
}

// TestUIMListMembers proves the members textarea is parsed to addresses and
// written to the directory for the named list.
func TestUIMListMembers(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		mlists: []directory.MListInfo{{Listname: "team@hermex.test"}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/mlists/team@hermex.test/members", session, csrf,
		url.Values{"members": {"alice@hermex.test\nbob@hermex.test"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set members status %d, want 200", resp.StatusCode)
	}
	if d.setMembersUser != "team@hermex.test" ||
		!slices.Equal(d.setMembers, []string{"alice@hermex.test", "bob@hermex.test"}) {
		t.Errorf("set members for %q = %v, want team@hermex.test [alice bob]", d.setMembersUser, d.setMembers)
	}
}

// TestUIDeleteMList proves deletion reaches the directory and redirects htmx back
// to the lists page.
func TestUIDeleteMList(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/mlists/team@hermex.test/delete", session, csrf, url.Values{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete list status %d, want 200", resp.StatusCode)
	}
	if d.deletedMList != "team@hermex.test" {
		t.Errorf("deleted %q, want team@hermex.test", d.deletedMList)
	}
	if loc := resp.Header.Get("HX-Redirect"); loc != "/admin/ui/mlists" {
		t.Errorf("HX-Redirect = %q, want /admin/ui/mlists", loc)
	}
}

// TestUIMListDetailRenders proves the detail page renders the list with its
// privilege label and current members (and catches template syntax errors).
func TestUIMListDetailRenders(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		mlists:       []directory.MListInfo{{Listname: "team@hermex.test", ListType: 0, ListPriv: 1}},
		mlistMembers: []string{"alice@hermex.test"},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/mlists/team@hermex.test", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"team@hermex.test", "Members only", "alice@hermex.test"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page missing %q", want)
		}
	}
}
