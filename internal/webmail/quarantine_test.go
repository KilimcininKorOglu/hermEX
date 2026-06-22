package webmail

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/quarantine"
)

var releaseSecret = []byte("test-release-secret-32-bytes-long!!!")

// releaseServer builds a webmail test server with a release secret and one mailbox.
func releaseServer(t *testing.T, maildir string, secret []byte) *httptest.Server {
	t.Helper()
	auth := directory.StaticAccounts{"alice@hermex.test": {Password: "secret", MailboxPath: maildir}}
	srv, err := NewServer(auth, auth, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	srv.DigestSecret = secret
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// seedJunk appends one Junk message and returns its UID.
func seedJunk(t *testing.T, maildir, subject string) uint32 {
	t.Helper()
	st, err := objectstore.Open(maildir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	info, err := st.AppendMessage(int64(mapi.PrivateFIDJunk),
		[]byte("Subject: "+subject+"\r\n\r\nspam"), time.Unix(1_700_000_000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.UID
}

// folderCount returns how many messages a folder holds.
func folderCount(t *testing.T, maildir string, folder int64) int {
	t.Helper()
	st, err := objectstore.Open(maildir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(folder)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

func releaseToken(t *testing.T, uid uint32, expiry time.Time) string {
	t.Helper()
	tok, err := quarantine.Mint(releaseSecret, quarantine.Claims{Mailbox: "alice@hermex.test", UID: uid, Expiry: expiry.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestReleaseGetConfirmsWithoutReleasing is the prefetch-safety test: a GET (which any
// mail scanner, proxy, or client preview issues) shows the confirmation form and must
// NOT move the message — otherwise the recipient's own security stack would auto-unfilter
// every quarantined spam.
func TestReleaseGetConfirmsWithoutReleasing(t *testing.T) {
	maildir := filepath.Join(t.TempDir(), "alice")
	uid := seedJunk(t, maildir, "cheap pills")
	ts := releaseServer(t, maildir, releaseSecret)

	resp, err := http.Get(ts.URL + "/quarantine/release?t=" + url.QueryEscape(releaseToken(t, uid, time.Now().Add(time.Hour))))
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); !strings.Contains(got, "Release to inbox") {
		t.Errorf("GET did not render the confirmation form:\n%s", got)
	}
	if c := folderCount(t, maildir, int64(mapi.PrivateFIDJunk)); c != 1 {
		t.Errorf("Junk count after GET = %d, want 1 (GET must not release)", c)
	}
	if c := folderCount(t, maildir, int64(mapi.PrivateFIDInbox)); c != 0 {
		t.Errorf("Inbox count after GET = %d, want 0 (GET must not release)", c)
	}
}

// TestReleasePostMovesToInbox proves the POST actually moves the message from Junk to
// the inbox — the discriminating check is that it LANDS in the inbox, not merely that it
// left Junk.
func TestReleasePostMovesToInbox(t *testing.T) {
	maildir := filepath.Join(t.TempDir(), "alice")
	uid := seedJunk(t, maildir, "you have won")
	ts := releaseServer(t, maildir, releaseSecret)

	resp, err := http.PostForm(ts.URL+"/quarantine/release",
		url.Values{"t": {releaseToken(t, uid, time.Now().Add(time.Hour))}})
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); !strings.Contains(got, "moved back to your inbox") {
		t.Errorf("POST did not acknowledge the release:\n%s", got)
	}
	if c := folderCount(t, maildir, int64(mapi.PrivateFIDInbox)); c != 1 {
		t.Errorf("Inbox count after release = %d, want 1 (message must land in the inbox)", c)
	}
	if c := folderCount(t, maildir, int64(mapi.PrivateFIDJunk)); c != 0 {
		t.Errorf("Junk count after release = %d, want 0", c)
	}
}

// TestReleaseStaleTokenIsBenign proves a second release of the same token reports the
// message already handled rather than erroring or moving the wrong message — Junk UIDs
// are never reused, so a stale token can only find nothing.
func TestReleaseStaleTokenIsBenign(t *testing.T) {
	maildir := filepath.Join(t.TempDir(), "alice")
	uid := seedJunk(t, maildir, "spam")
	ts := releaseServer(t, maildir, releaseSecret)
	tok := releaseToken(t, uid, time.Now().Add(time.Hour))

	http.PostForm(ts.URL+"/quarantine/release", url.Values{"t": {tok}}) // first release
	resp, err := http.PostForm(ts.URL+"/quarantine/release", url.Values{"t": {tok}})
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); !strings.Contains(got, "already been handled") {
		t.Errorf("second release should be benign:\n%s", got)
	}
	if c := folderCount(t, maildir, int64(mapi.PrivateFIDInbox)); c != 1 {
		t.Errorf("Inbox count after a repeated release = %d, want 1 (no duplicate)", c)
	}
}

// TestReleaseInvalidAndExpired proves a tampered token is rejected as invalid and an
// expired one names its expiry, and that neither moves anything.
func TestReleaseInvalidAndExpired(t *testing.T) {
	maildir := filepath.Join(t.TempDir(), "alice")
	uid := seedJunk(t, maildir, "spam")
	ts := releaseServer(t, maildir, releaseSecret)

	resp, _ := http.PostForm(ts.URL+"/quarantine/release", url.Values{"t": {"not-a-valid-token"}})
	if got := body(t, resp); !strings.Contains(got, "invalid") {
		t.Errorf("garbage token should be invalid:\n%s", got)
	}
	expired := releaseToken(t, uid, time.Now().Add(-time.Hour))
	resp, _ = http.PostForm(ts.URL+"/quarantine/release", url.Values{"t": {expired}})
	if got := body(t, resp); !strings.Contains(got, "expired") {
		t.Errorf("past-expiry token should report expiry:\n%s", got)
	}
	if c := folderCount(t, maildir, int64(mapi.PrivateFIDJunk)); c != 1 {
		t.Errorf("Junk count after rejected tokens = %d, want 1 (nothing released)", c)
	}
}

// TestReleaseDisabledWithoutSecret proves the endpoint 404s when no signing secret is
// configured, so the feature is fully off.
func TestReleaseDisabledWithoutSecret(t *testing.T) {
	maildir := filepath.Join(t.TempDir(), "alice")
	seedJunk(t, maildir, "spam")
	ts := releaseServer(t, maildir, nil)

	resp, err := http.Get(ts.URL + "/quarantine/release?t=anything")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("release without a secret = %d, want 404", resp.StatusCode)
	}
}
