package webmail

import (
	"io"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
)

// TestWebmailComposeRelaysExternal proves a composed message to a foreign-domain
// recipient is queued in the relay spool rather than dropped: with a spool set,
// the external recipient leaves through outbound relay carrying the sender's
// address as the envelope From.
func TestWebmailComposeRelaysExternal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	if st, err := objectstore.Open(path); err != nil {
		t.Fatal(err)
	} else {
		st.Close()
	}
	auth := directory.StaticAccounts{"alice@hermex.test": {Password: "secret", MailboxPath: path}}
	srv, err := NewServer(auth, auth, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	srv.Spool = sp

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := authedClient(t, ts)

	resp, err := c.PostForm(ts.URL+"/compose", url.Values{
		"to":      {"bob@external.test"},
		"subject": {"hi"},
		"body":    {"hello bob"},
	})
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	due, err := sp.Claim(time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Recipient != "bob@external.test" {
		t.Fatalf("relay spool = %v, want bob@external.test queued for relay", due)
	}
	if due[0].From != "alice@hermex.test" {
		t.Errorf("relay envelope From = %q, want alice@hermex.test", due[0].From)
	}
}
