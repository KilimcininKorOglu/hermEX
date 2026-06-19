package admin

import (
	"io"
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
	body, _ := io.ReadAll(resp.Body)
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
