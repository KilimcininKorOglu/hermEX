package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// oofUserDir is a system-admin directory whose one user has a known maildir, so
// the OOF handlers resolve the mailbox store path.
func oofUserDir() *fakeDir {
	return &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test", Maildir: "/mb/alice"},
	}
}

// TestAdminGetUserOOF proves a system admin reads a user's out-of-office settings,
// resolved from the mailbox store at the user's maildir and serialized in the
// canonical encoding — the external audience and the per-audience subject survive.
func TestAdminGetUserOOF(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{oof: map[string]objectstore.OOFSettings{
		"/mb/alice": {Enabled: true, InternalSubject: "Away until Monday", ExternalEnabled: true, ExternalAudience: objectstore.OOFExternalKnown},
	}}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/oof", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get oof status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"internalSubject":"Away until Monday"`) || !strings.Contains(string(body), `"externalAudience":1`) {
		t.Errorf("oof body = %s, want the canonical settings", body)
	}
}

// TestAdminSetUserOOF proves a system admin writes a user's out-of-office settings
// through to the mailbox store at the user's maildir, with the known-only audience
// preserved.
func TestAdminSetUserOOF(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	body := `{"enabled":true,"internalSubject":"Away","internalReply":"Back Monday","externalEnabled":true,"externalSubject":"OOO","externalReply":"Away","externalAudience":1,"start":1700000000,"end":1700600000}`
	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/oof", session, csrf, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set oof status %d, want 204", resp.StatusCode)
	}
	if store.setDir != "/mb/alice" {
		t.Errorf("SetOOFSettings maildir = %q, want /mb/alice", store.setDir)
	}
	got := store.setOOF
	if !got.Enabled || got.InternalSubject != "Away" || got.ExternalSubject != "OOO" ||
		!got.ExternalEnabled || got.ExternalAudience != objectstore.OOFExternalKnown || got.Start != 1700000000 {
		t.Errorf("stored oof = %+v, want the submitted settings", got)
	}
}

// TestAdminUserOOFNotFound proves reading or writing an unknown user's OOF is a 404
// and the store is never touched for a user that does not exist.
func TestAdminUserOOFNotFound(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}, getUserMissing: true}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/ghost@hermex.test/oof", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/users/ghost@hermex.test/oof", session, csrf, `{"enabled":true}`)
	put.Body.Close()
	if get.StatusCode != http.StatusNotFound || put.StatusCode != http.StatusNotFound {
		t.Errorf("unknown user oof = GET %d / PUT %d, want both 404", get.StatusCode, put.StatusCode)
	}
	if store.setDir != "" {
		t.Errorf("an unknown user still wrote to the store at %q", store.setDir)
	}
}

// TestAdminUserOOFRequiresSystem proves a domain admin (not a system admin) cannot
// read or write a user's OOF through the system-scoped endpoints.
func TestAdminUserOOFRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/oof", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/users/alice@hermex.test/oof", session, csrf, `{"enabled":true}`)
	put.Body.Close()
	if get.StatusCode != http.StatusForbidden || put.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin oof = GET %d / PUT %d, want both 403", get.StatusCode, put.StatusCode)
	}
}

// TestUIUserDetailShowsOOF proves the detail page renders the out-of-office section
// populated from the mailbox store: the section, the form fields, the stored
// subject, and the known-only audience control are present.
func TestUIUserDetailShowsOOF(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{oof: map[string]objectstore.OOFSettings{
		"/mb/alice": {Enabled: true, InternalSubject: "On holiday", ExternalEnabled: true, ExternalAudience: objectstore.OOFExternalKnown},
	}}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"Out of office", `name="internalsubject"`, "On holiday", `name="externalknownonly"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page OOF section missing %q", want)
		}
	}
}

// TestUIUserOOF proves the detail-form save writes the OOF settings through to the
// store, mapping the known-only checkbox onto the external audience, and reports
// success.
func TestUIUserOOF(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/oof", session, csrf, url.Values{
		"enabled":           {"on"},
		"internalsubject":   {"Away"},
		"internalreply":     {"Back Monday"},
		"externalenabled":   {"on"},
		"externalsubject":   {"OOO"},
		"externalreply":     {"Away from email"},
		"externalknownonly": {"on"},
		"start":             {"2026-06-01T09:00"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui oof save status %d, want 200", resp.StatusCode)
	}
	if store.setDir != "/mb/alice" {
		t.Errorf("SetOOFSettings maildir = %q, want /mb/alice", store.setDir)
	}
	got := store.setOOF
	if !got.Enabled || got.InternalSubject != "Away" || !got.ExternalEnabled || got.ExternalSubject != "OOO" ||
		got.ExternalAudience != objectstore.OOFExternalKnown || got.Start == 0 {
		t.Errorf("stored oof = %+v, want the submitted form settings", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("ui oof save did not report success:\n%s", body)
	}
}
