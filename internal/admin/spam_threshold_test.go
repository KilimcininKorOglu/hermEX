package admin

import (
	"io"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestSaveUserSpamThreshold proves the per-user threshold form persists a value,
// clears it on an empty field (inherit), and rejects a value below 1.
func TestSaveUserSpamThreshold(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@test/spam-threshold", session, csrf, url.Values{"spam_threshold": {"5"}})
	resp.Body.Close()
	if !d.userSpamThresholdSet || d.userSpamThreshold == nil || *d.userSpamThreshold != 5 {
		t.Fatalf("user threshold persisted = set %v val %v, want 5", d.userSpamThresholdSet, d.userSpamThreshold)
	}

	d.userSpamThresholdSet, d.userSpamThreshold = false, nil
	resp = htmxPUT(t, ts, "/admin/ui/users/alice@test/spam-threshold", session, csrf, url.Values{"spam_threshold": {""}})
	resp.Body.Close()
	if !d.userSpamThresholdSet || d.userSpamThreshold != nil {
		t.Errorf("empty field must clear the override; set=%v val=%v", d.userSpamThresholdSet, d.userSpamThreshold)
	}

	d.userSpamThresholdSet = false
	resp = htmxPUT(t, ts, "/admin/ui/users/alice@test/spam-threshold", session, csrf, url.Values{"spam_threshold": {"0"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if d.userSpamThresholdSet {
		t.Error("a threshold below 1 must not be persisted")
	}
	if !strings.Contains(string(body), "at least 1") {
		t.Errorf("expected a validation message:\n%s", body)
	}
}

// TestSaveDomainSpamThreshold proves the per-domain threshold form persists a value,
// keyed by the domain name resolved from the path's domain id.
func TestSaveDomainSpamThreshold(t *testing.T) {
	d := &fakeDir{
		authOK:       true,
		uid:          7,
		roles:        []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "hermex.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/domains/1/spam-threshold", session, csrf, url.Values{"spam_threshold": {"9"}})
	resp.Body.Close()
	if !d.domainSpamThresholdSet || d.domainSpamThreshold == nil || *d.domainSpamThreshold != 9 {
		t.Fatalf("domain threshold persisted = set %v val %v, want 9", d.domainSpamThresholdSet, d.domainSpamThreshold)
	}
}
