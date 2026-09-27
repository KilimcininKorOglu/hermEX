package admin

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// statusServer builds an admin server over a directory holding the given health
// targets.
func statusServer(t *testing.T, d *fakeDir, targets []directory.HealthTarget) *httptest.Server {
	t.Helper()
	d.healthTargets = targets
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// daemonAnswering serves a fixed /healthz answer with the given status.
func daemonAnswering(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// refusingAddr returns an address that accepts and drops every connection, held
// open for the whole test so no other test server can reuse the port and turn the
// intended failure into a stray reply.
func refusingAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().String()
}

// systemDir is a directory whose signed-in operator is a full system admin.
func systemDir() *fakeDir {
	return &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
}

// TestLiveStatusProbe proves the Live status page and JSON classify a reachable
// healthy daemon as Up and an unreachable one as Down.
func TestLiveStatusProbe(t *testing.T) {
	healthy := daemonAnswering(t, http.StatusOK, `{"service":"imap","version":"t","uptime_seconds":42,"ok":true}`)
	targets := []directory.HealthTarget{
		{ID: 1, Name: "imap", URL: healthy.URL + "/healthz"},
		{ID: 2, Name: "mta", URL: "http://" + refusingAddr(t) + "/healthz"},
	}
	ts := statusServer(t, systemDir(), targets)
	session, _ := loginCookies(t, ts)

	page := wantBody(t, authedGET(t, ts, "/admin/ui/status", session), http.StatusOK, "status page")
	for _, want := range []string{"imap", "Up", "mta", "Down"} {
		wantContains(t, page, want, "the page reports each daemon and its state")
	}

	resp := authedGET(t, ts, "/admin/status", session)
	var got []struct{ Name, Status string }
	err := json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	mustNoErr(t, err, "decode status JSON")
	byName := map[string]string{}
	for _, r := range got {
		byName[r.Name] = r.Status
	}
	wantEq(t, byName["imap"], "Up", "the reachable daemon's status")
	if byName["mta"] != "Down" {
		t.Errorf("mta status = %q, want Down", byName["mta"])
	}
}

// TestDegradedNamesTheFailedCheck proves a degraded daemon's row says which
// readiness check failed and why, which is the question the operator opens the
// page with.
func TestDegradedNamesTheFailedCheck(t *testing.T) {
	sick := daemonAnswering(t, http.StatusServiceUnavailable,
		`{"service":"imap","version":"t","uptime_seconds":5,"ok":false,"checks":{"directory":"dial tcp: connection refused","spool":"ok"}}`)
	ts := statusServer(t, systemDir(), []directory.HealthTarget{{ID: 1, Name: "imap", URL: sick.URL + "/healthz"}})
	session, _ := loginCookies(t, ts)

	page := wantBody(t, authedGET(t, ts, "/admin/ui/status/panel", session), http.StatusOK, "status panel")
	wantContains(t, page, "Degraded", "the daemon is degraded")
	wantContains(t, page, "directory: dial tcp: connection refused", "the failed check and its error")
	if strings.Contains(page, "spool") {
		t.Errorf("a passing check is listed as a failure:\n%s", page)
	}
}

// TestHealthTargetsAreManagedFromThePanel proves a target added in the panel is
// probed on the next refresh and a removed one stops being probed, with no
// restart in between.
func TestHealthTargetsAreManagedFromThePanel(t *testing.T) {
	healthy := daemonAnswering(t, http.StatusOK, `{"version":"t","uptime_seconds":1,"ok":true}`)
	d := systemDir()
	ts := statusServer(t, d, nil)
	session, csrf := loginCookies(t, ts)

	empty := wantBody(t, authedGET(t, ts, "/admin/ui/status/panel", session), http.StatusOK, "empty panel")
	wantContains(t, empty, "No health targets configured", "the empty state")

	add := htmxPOST(t, ts, "/admin/ui/status/targets", session, csrf,
		url.Values{"name": {"imap"}, "url": {healthy.URL + "/healthz"}})
	add.Body.Close()
	wantEq(t, add.StatusCode, http.StatusOK, "the add answers the refreshed table")
	if add.Header.Get("HX-Trigger") != "health-targets-changed" {
		t.Errorf("the add does not ask the page to refresh the status table")
	}
	panel := wantBody(t, authedGET(t, ts, "/admin/ui/status/panel", session), http.StatusOK, "panel after add")
	wantContains(t, panel, "imap", "the added daemon is probed")
	wantContains(t, panel, "Up", "the added daemon is up")

	del := htmxPOST(t, ts, "/admin/ui/status/targets/1/delete", session, csrf, url.Values{})
	del.Body.Close()
	after := wantBody(t, authedGET(t, ts, "/admin/ui/status/panel", session), http.StatusOK, "panel after delete")
	wantContains(t, after, "No health targets configured", "the removed daemon is no longer probed")
}

// TestHealthTargetAddRefusals proves an unusable or duplicate target is refused
// with its reason and nothing is stored.
func TestHealthTargetAddRefusals(t *testing.T) {
	d := systemDir()
	ts := statusServer(t, d, []directory.HealthTarget{{ID: 1, Name: "imap", URL: "http://imap:8090/healthz"}})
	session, csrf := loginCookies(t, ts)

	bad := wantBody(t, htmxPOST(t, ts, "/admin/ui/status/targets", session, csrf, url.Values{"name": {""}, "url": {"x"}}),
		http.StatusOK, "invalid target")
	wantContains(t, bad, "Enter a name and an http or https URL", "the invalid-target reason")
	dup := wantBody(t, htmxPOST(t, ts, "/admin/ui/status/targets", session, csrf,
		url.Values{"name": {"imap"}, "url": {"http://other:8090/healthz"}}), http.StatusOK, "duplicate target")
	wantContains(t, dup, "already monitored", "the duplicate-name reason")
	wantEq(t, len(d.healthTargets), 1, "nothing new was stored")
}

// TestHealthTargetReadFailure proves an unreadable target list shows as a
// failure and is not rendered as an empty monitor.
func TestHealthTargetReadFailure(t *testing.T) {
	d := systemDir()
	d.readErrs = map[string]error{"ListHealthTargets": errors.New("db down")}
	ts := statusServer(t, d, nil)
	session, _ := loginCookies(t, ts)
	page := wantBody(t, authedGET(t, ts, "/admin/ui/status", session), http.StatusOK, "status page")
	wantContains(t, page, "Could not read the monitored daemons.", "the read failure")
	if strings.Contains(page, "No health targets configured") {
		t.Errorf("a failed read shows as an empty monitor:\n%s", page)
	}
	resp := authedGET(t, ts, "/admin/status", session)
	resp.Body.Close()
	wantEq(t, resp.StatusCode, http.StatusInternalServerError, "the JSON status on a failed read")
}

// TestLiveStatusRequiresSystem proves an org admin can neither view the Live
// status page nor change what it monitors.
func TestLiveStatusRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts := statusServer(t, d, nil)
	session, csrf := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/status", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin status page = %d, want 403", resp.StatusCode)
	}
	add := htmxPOST(t, ts, "/admin/ui/status/targets", session, csrf,
		url.Values{"name": {"imap"}, "url": {"http://imap:8090/healthz"}})
	add.Body.Close()
	if add.StatusCode != http.StatusForbidden || len(d.healthTargets) != 0 {
		t.Errorf("org-admin add = %d, stored %d; want 403 and nothing stored", add.StatusCode, len(d.healthTargets))
	}
}
