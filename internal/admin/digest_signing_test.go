package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// digestPanelServer builds an admin server whose digest signing secret is either
// configured or not, the one fact that decides whether the digest can send.
func digestPanelServer(t *testing.T, d Directory, signing bool) *httptest.Server {
	t.Helper()
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.SetDigestSigning(signing)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// saveDigest posts the digest form and returns the rendered panel.
func saveDigest(t *testing.T, ts *httptest.Server, enabled bool) string {
	t.Helper()
	session, csrf := loginCookies(t, ts)
	form := url.Values{"interval": {"12"}, "base_url": {"https://mail.example.com"}}
	if enabled {
		form.Set("enabled", "1")
	}
	resp := htmxPOST(t, ts, "/admin/ui/antispam/digest", session, csrf, form)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save = %d, want 200", resp.StatusCode)
	}
	return string(body)
}

// systemAdmin is a directory that authenticates one full system administrator.
func systemAdmin() *fakeDir {
	return &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
}

// TestDigestPanelWarnsWithNoSigningSecret proves the panel stops reporting a
// feature active that can never fire. Every digest entry carries a signed release
// link, so with no secret nothing is sent at all; the panel read the stored
// toggle alone and told the operator users were receiving summaries, while
// quarantined legitimate mail sat unnoticed.
func TestDigestPanelWarnsWithNoSigningSecret(t *testing.T) {
	body := saveDigest(t, digestPanelServer(t, systemAdmin(), false), true)

	if !strings.Contains(body, "digest_secret") {
		t.Errorf("the panel does not mention the missing secret:\n%s", body)
	}
	if !strings.Contains(body, "no summary is sent") && !strings.Contains(body, "nothing will be sent") {
		t.Errorf("the panel does not say that nothing is sent:\n%s", body)
	}
	if strings.Contains(body, "<strong>on</strong>") {
		t.Errorf("the digest is still rendered as plainly on:\n%s", body)
	}
}

// TestDigestPanelIsQuietWhenItCanSend guards the other direction: with a secret
// configured the warning must not appear, or it becomes noise the operator learns
// to ignore.
func TestDigestPanelIsQuietWhenItCanSend(t *testing.T) {
	body := saveDigest(t, digestPanelServer(t, systemAdmin(), true), true)

	if strings.Contains(body, "no summary is sent") || strings.Contains(body, "nothing will be sent") {
		t.Errorf("a working digest is reported as unable to send:\n%s", body)
	}
	if !strings.Contains(body, "<strong>on</strong>") {
		t.Errorf("a working digest is not reported as on:\n%s", body)
	}
	if !strings.Contains(body, "Digest settings saved") {
		t.Errorf("response missing acknowledgment:\n%s", body)
	}
}

// TestDigestSaveIsHonestAboutWhatItDid proves the acknowledgment matches reality.
// Saving used to answer that the MTA would apply the settings within a minute,
// which is true of the settings and false of the outcome when nothing can sign.
func TestDigestSaveIsHonestAboutWhatItDid(t *testing.T) {
	body := saveDigest(t, digestPanelServer(t, systemAdmin(), false), true)
	if !strings.Contains(body, "nothing will be sent") {
		t.Errorf("the save acknowledgment claims an effect it did not have:\n%s", body)
	}
}

// TestDigestWarningShowsEvenWhenOff proves the operator can see the missing
// prerequisite before turning the digest on, rather than after.
func TestDigestWarningShowsEvenWhenOff(t *testing.T) {
	body := saveDigest(t, digestPanelServer(t, systemAdmin(), false), false)
	if !strings.Contains(body, "digest_secret") {
		t.Errorf("the prerequisite is only mentioned once it is too late:\n%s", body)
	}
}
