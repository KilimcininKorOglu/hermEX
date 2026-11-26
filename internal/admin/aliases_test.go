package admin

import (
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestAdminListAliases proves a system admin lists every alias.
func TestAdminListAliases(t *testing.T) {
	d := &fakeDir{
		authOK:  true,
		uid:     7,
		roles:   []directory.AdminRole{{Role: directory.AdminSystem}},
		aliases: []directory.AliasInfo{{ID: 1, Alias: "sales@hermex.test", Main: "boss@hermex.test"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/aliases", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list aliases status %d, want 200", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(string(body), "sales@hermex.test") || !strings.Contains(string(body), "boss@hermex.test") {
		t.Errorf("list body = %s, want the alias mapping", body)
	}
}

// TestAdminCreateAlias proves a system admin creates an alias forwarding to a
// primary address.
func TestAdminCreateAlias(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/aliases", session, csrf,
		`{"alias":"sales@hermex.test","main":"boss@hermex.test"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create alias status %d, want 201", resp.StatusCode)
	}
	if d.createdAlias != "sales@hermex.test" || d.createdAliasTo != "boss@hermex.test" {
		t.Errorf("created alias %q -> %q, want sales -> boss", d.createdAlias, d.createdAliasTo)
	}
}

// TestAdminCreateAliasNeedsBothAddresses sends alias requests missing a side or
// not JSON at all: each is a 400 and the directory is never asked to create an
// alias that points nowhere or has no name.
func TestAdminCreateAliasNeedsBothAddresses(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	for _, body := range []string{`{"alias":"sales@hermex.test"}`, `{"main":"boss@hermex.test"}`, `{"alias":"","main":""}`, `not json`} {
		resp := authedPOST(t, ts, "/admin/aliases", session, csrf, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, resp.StatusCode)
		}
	}
	if d.createdAlias != "" || d.createdAliasTo != "" {
		t.Errorf("an incomplete request created alias %q -> %q", d.createdAlias, d.createdAliasTo)
	}
}
