package activesync

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
	"hermex/internal/wbxml"
)

// TestSendMailRelaysExternal proves a SendMail to a foreign-domain recipient is
// queued for outbound relay rather than dropped: with a spool set, the external
// recipient leaves through the relay carrying the sender's address.
func TestSendMailRelaysExternal(t *testing.T) {
	aliceDir := filepath.Join(t.TempDir(), "alice")
	if st, err := objectstore.Open(aliceDir); err != nil {
		t.Fatal(err)
	} else {
		st.Close()
	}
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: aliceDir}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	srv.Spool = sp

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	mime := "From: alice@hermex.test\r\nTo: carol@external.test\r\nSubject: Out\r\n\r\nhi carol\r\n"
	sm := wbxml.Elem(wbxml.CMSendMail,
		wbxml.Str(wbxml.CMClientID, "c1"),
		wbxml.Opaque(wbxml.CMMIME, []byte(mime)))

	resp, out := postRaw(t, ts, "SendMail", wbxml.Marshal(sm))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, out)
	}
	due, err := sp.Claim(time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Recipient != "carol@external.test" {
		t.Fatalf("relay spool = %v, want carol@external.test queued for relay", due)
	}
	if due[0].From != testUser {
		t.Errorf("relay envelope From = %q, want %q", due[0].From, testUser)
	}
}
