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

// relayServer starts an ActiveSync server for the test user with an outbound relay
// spool, and returns both.
func relayServer(t *testing.T) (*httptest.Server, *relay.Spool) {
	t.Helper()
	aliceDir := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	accs := directory.StaticAccounts{
		testUser: {Password: testPass, MailboxPath: aliceDir},
		bobUser:  {Password: "x", MailboxPath: filepath.Join(t.TempDir(), "bob")},
	}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sp.Close(); err != nil {
			t.Error(err)
		}
	})
	srv.Spool = sp
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, sp
}

// TestSendMailRelaysNothingUnderAForgedFrom proves a From the caller may not use
// never reaches the relay spool: the relay worker DKIM-signs what it sends for the
// From domain, so a forged From that got queued would leave signed as another
// user. The refusal is 403, as for local delivery.
func TestSendMailRelaysNothingUnderAForgedFrom(t *testing.T) {
	for _, tc := range []struct{ name, from string }{
		{"another user", "From: " + bobUser + "\r\n"},
		// RFC 5322 3.6.2: the caller's own address beside another's in one field.
		{"a list naming another user", "From: " + testUser + ", " + bobUser + "\r\n"},
		// RFC 5322 3.6 allows one From field; a second one names another user.
		{"a second From field", "From: " + testUser + "\r\nFrom: " + bobUser + "\r\n"},
		// A field that does not parse cannot be shown to be the caller's.
		{"an unparseable field", "From: " + bobUser + " <\r\n"},
		// Nor can a From in a header block that does not parse at all.
		{"a header block that does not parse", "no colon here\r\nFrom: " + bobUser + "\r\n"},
	} {
		ts, sp := relayServer(t)
		forged := tc.from + "To: carol@external.test\r\nSubject: Out\r\n\r\nhi carol\r\n"
		sm := wbxml.Elem(wbxml.CMSendMail,
			wbxml.Str(wbxml.CMClientID, "c1"),
			wbxml.Opaque(wbxml.CMMIME, []byte(forged)))
		// Refused either way: 403 at the From gate, or 400 earlier when a header
		// block that does not parse yields no recipients at all.
		if resp, _ := postRaw(t, ts, "SendMail", wbxml.Marshal(sm)); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want a refusal (403 or 400)", tc.name, resp.StatusCode)
		}
		due, err := sp.Claim(time.Now(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(due) != 0 {
			t.Errorf("%s: relay spool holds %d messages, want 0", tc.name, len(due))
		}
	}
}

// TestSendMailRelaysAMessageWithoutFrom proves a message that claims no identity
// still goes out: it names nobody to forge, and the envelope keeps the caller.
func TestSendMailRelaysAMessageWithoutFrom(t *testing.T) {
	ts, sp := relayServer(t)
	sm := wbxml.Elem(wbxml.CMSendMail,
		wbxml.Str(wbxml.CMClientID, "c1"),
		wbxml.Opaque(wbxml.CMMIME, []byte("To: carol@external.test\r\nSubject: Out\r\n\r\nhi carol\r\n")))
	if resp, out := postRaw(t, ts, "SendMail", wbxml.Marshal(sm)); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, out)
	}
	due, err := sp.Claim(time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].From != testUser {
		t.Errorf("relay spool = %v, want one message with envelope From %s", due, testUser)
	}
}

// TestSendMailRelaysExternal proves a SendMail to a foreign-domain recipient is
// queued for outbound relay rather than dropped: with a spool set, the external
// recipient leaves through the relay carrying the sender's address.
func TestSendMailRelaysExternal(t *testing.T) {
	ts, sp := relayServer(t)
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
