package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	down.Close() // closed: connections are refused, so the probe reads Down

	targets := []HealthTarget{
		{Name: "imap", URL: healthy.URL + "/healthz"},
		{Name: "mta", URL: down.URL + "/healthz"},
	}
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := statusServer(t, d, targets)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/status", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status page = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	if !strings.Contains(page, "imap") || !strings.Contains(page, "Up") {
		t.Errorf("page missing healthy imap/Up:\n%s", page)
	}
	if !strings.Contains(page, "mta") || !strings.Contains(page, "Down") {
		t.Errorf("page missing down mta/Down:\n%s", page)
	}

	resp = authedGET(t, ts, "/admin/status", session)
	var got []struct{ Name, Status string }
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode status JSON: %v", err)
	}
	resp.Body.Close()
	byName := map[string]string{}
	for _, r := range got {
		byName[r.Name] = r.Status
	}
	if byName["imap"] != "Up" {
		t.Errorf("imap status = %q, want Up", byName["imap"])
	}
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
