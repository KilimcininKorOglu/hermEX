package admin

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"hermex/internal/directory"
)

// statusServer builds an admin server configured to probe the given health
// targets.
func statusServer(t *testing.T, d Directory, targets []HealthTarget) *httptest.Server {
	t.Helper()
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.SetHealthTargets(targets)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestLiveStatusProbe proves the Live status page and JSON classify a reachable
// healthy daemon as Up and an unreachable one as Down.
func TestLiveStatusProbe(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"imap","version":"t","uptime_seconds":42,"ok":true}`))
	}))
	defer healthy.Close()
	// down holds its listener open for the whole test so the port can never be
	// reused by another test server. Such reuse used to turn the intended refusal
	// into a stray reply (Degraded), making this test flaky. Every accepted
	// connection is dropped without an HTTP response, so the probe's client.Do
	// fails and the daemon is classified Down deterministically.
	downLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer downLn.Close()
	go func() {
		for {
			c, aerr := downLn.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close()
		}
	}()

	targets := []HealthTarget{
		{Name: "imap", URL: healthy.URL + "/healthz"},
		{Name: "mta", URL: "http://" + downLn.Addr().String() + "/healthz"},
	}
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := statusServer(t, d, targets)
	session, _ := loginCookies(t, ts)

	page := wantBody(t, authedGET(t, ts, "/admin/ui/status", session), http.StatusOK, "status page")
	for _, want := range []string{"imap", "Up", "mta", "Down"} {
		wantContains(t, page, want, "the page reports each daemon and its state")
	}

	resp := authedGET(t, ts, "/admin/status", session)
	var got []struct{ Name, Status string }
	err = json.NewDecoder(resp.Body).Decode(&got)
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

// TestLiveStatusRequiresSystem proves an org admin cannot reach the Live status page.
func TestLiveStatusRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts := statusServer(t, d, nil)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/status", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin status page = %d, want 403", resp.StatusCode)
	}
}
