package admin

import (
	"net/http"
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

	wantStatus(t, authedGET(t, ts, "/admin/aliases", ""), http.StatusUnauthorized, "unauth aliases GET")
	body := wantBody(t, authedGET(t, ts, "/admin/aliases", session), http.StatusOK, "list aliases")
	wantContains(t, body, "sales@hermex.test", "list aliases")
	wantContains(t, body, "boss@hermex.test", "list aliases")
}

// TestAdminCreateAlias proves a system admin creates an alias forwarding to a
// primary address.
func TestAdminCreateAlias(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPOST(t, ts, "/admin/aliases", "", "",
		`{"alias":"sales@hermex.test","main":"boss@hermex.test"}`), http.StatusUnauthorized, "unauth create alias")
	wantFalse(t, d.createdAlias != "", "unauth request created alias")

	wantStatus(t, authedPOST(t, ts, "/admin/aliases", session, csrf,
		`{"alias":"sales@hermex.test","main":"boss@hermex.test"}`), http.StatusCreated, "create alias")
	wantEq(t, d.createdAlias, "sales@hermex.test", "created alias")
	wantEq(t, d.createdAliasTo, "boss@hermex.test", "created alias target")
}

// TestAdminCreateAliasNeedsBothAddresses sends alias requests missing a side or
// not JSON at all: each is a 400 and the directory is never asked to create an
// alias that points nowhere or has no name.
func TestAdminCreateAliasNeedsBothAddresses(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	for _, body := range []string{`{"alias":"sales@hermex.test"}`, `{"main":"boss@hermex.test"}`, `{"alias":"","main":""}`, `not json`} {
		wantStatus(t, authedPOST(t, ts, "/admin/aliases", session, csrf, body), http.StatusBadRequest, body)
	}
	wantEq(t, d.createdAlias, "", "incomplete request created alias")
	wantEq(t, d.createdAliasTo, "", "incomplete request created alias target")
}
