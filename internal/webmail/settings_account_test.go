package webmail

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// multiIdentityDir is a static directory whose users may send as more than just
// themselves, for testing the account-identities widget.
type multiIdentityDir struct {
	directory.StaticAccounts
	ids []string
}

func (d multiIdentityDir) Identities(string) ([]string, error) { return d.ids, nil }

// TestSettingsAccountIdentities checks the settings Account widget shows the login
// and the other addresses the account may send as, excluding the login itself.
func TestSettingsAccountIdentities(t *testing.T) {
	path := emptyMailbox(t)
	auth := multiIdentityDir{
		StaticAccounts: directory.StaticAccounts{"alice@hermex.test": {Password: "secret", MailboxPath: path}},
		ids:            []string{"alice@hermex.test", "sales@hermex.test"},
	}
	srv, err := NewServer(auth, auth, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	resp, err := cl.PostForm(ts.URL+"/login", url.Values{"user": {"alice@hermex.test"}, "password": {"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	code, body := get(t, cl, ts.URL+"/settings")
	if code != 200 {
		t.Fatalf("settings status %d, want 200", code)
	}
	if !strings.Contains(body, "Signed in as <strong>alice@hermex.test</strong>") {
		t.Errorf("settings page missing the account email")
	}
	// "also send as: sales@hermex.test" (and not starting with alice) confirms the
	// alias is listed and the login itself is excluded.
	if !strings.Contains(body, "also send as: sales@hermex.test") {
		t.Errorf("account widget did not list the send-as alias or did not exclude self:\n%s", body)
	}
}
