package activesync

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

const bobUser = "bob@hermex.test"

// sendServer starts a server with two seeded mailboxes (alice the sender, bob a
// local recipient) so SendMail can deliver locally. It authorizes alice.
func sendServer(t *testing.T) (ts *httptest.Server, aliceDir, bobDir string) {
	t.Helper()
	aliceDir = filepath.Join(t.TempDir(), "alice")
	bobDir = filepath.Join(t.TempDir(), "bob")
	for _, d := range []string{aliceDir, bobDir} {
		st, err := objectstore.Open(d)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	accs := directory.StaticAccounts{
		testUser: {Password: testPass, MailboxPath: aliceDir},
		bobUser:  {Password: "bobpass", MailboxPath: bobDir},
	}
	ts = httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, aliceDir, bobDir
}

// postRaw POSTs a command body and returns the raw (undecoded) response, for
// commands like SendMail whose success reply is an empty body.
func postRaw(t *testing.T, ts *httptest.Server, cmd string, body []byte) (*http.Response, []byte) {
	t.Helper()
	url := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=" + cmd + "&User=" + testUser + "&DeviceId=dev1&DeviceType=iPhone"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, testPass)
	req.Header.Set("Content-Type", "application/vnd.ms-sync.wbxml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func folderCount(t *testing.T, dir string, fid int64) int {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(fid)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

const sampleMIME = "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: Hi Bob\r\n" +
	"Date: Mon, 15 Jun 2026 09:00:00 +0000\r\nMessage-ID: <s1@hermex.test>\r\n\r\nHello Bob\r\n"

// TestSendMailDelivers confirms a ComposeMail SendMail delivers to the local
// recipient's Inbox and, with SaveInSentItems, saves a copy to the sender's Sent.
func TestSendMailDelivers(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	sm := wbxml.Elem(wbxml.CMSendMail,
		wbxml.Str(wbxml.CMClientID, "c1"),
		wbxml.Empty(wbxml.CMSaveInSentItems),
		wbxml.Opaque(wbxml.CMMIME, []byte(sampleMIME)))

	resp, out := postRaw(t, ts, "SendMail", wbxml.Marshal(sm))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, out)
	}
	if len(out) != 0 {
		t.Errorf("SendMail success should have an empty body, got %d bytes", len(out))
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("bob's Inbox has %d messages, want 1", n)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDSentItems)); n != 1 {
		t.Errorf("alice's Sent has %d messages, want 1", n)
	}
}

// TestSendMailNoSentCopy confirms a SendMail without SaveInSentItems delivers
// but saves no Sent copy.
func TestSendMailNoSentCopy(t *testing.T) {
	ts, aliceDir, bobDir := sendServer(t)
	sm := wbxml.Elem(wbxml.CMSendMail, wbxml.Opaque(wbxml.CMMIME, []byte(sampleMIME)))

	resp, _ := postRaw(t, ts, "SendMail", wbxml.Marshal(sm))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("bob's Inbox has %d messages, want 1", n)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDSentItems)); n != 0 {
		t.Errorf("alice's Sent has %d messages, want 0 (SaveInSentItems absent)", n)
	}
}

// TestSendMailLegacyRawMIME confirms the pre-14 path (a raw-MIME body, no WBXML
// envelope) also delivers.
func TestSendMailLegacyRawMIME(t *testing.T) {
	ts, _, bobDir := sendServer(t)
	resp, _ := postRaw(t, ts, "SendMail", []byte(sampleMIME))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("bob's Inbox has %d messages, want 1", n)
	}
}

// TestStripBcc confirms the Bcc field and its folded continuation are removed
// while the other headers and the body survive.
func TestStripBcc(t *testing.T) {
	raw := []byte("To: bob@hermex.test\r\nBcc: carol@hermex.test,\r\n dan@hermex.test\r\nSubject: x\r\n\r\nbody\r\n")
	out := stripBcc(raw)
	if bytes.Contains(bytes.ToLower(out), []byte("bcc:")) {
		t.Error("Bcc field not removed")
	}
	for _, gone := range []string{"carol", "dan"} {
		if bytes.Contains(out, []byte(gone)) {
			t.Errorf("Bcc recipient %q survived (folded continuation not stripped)", gone)
		}
	}
	if !bytes.Contains(out, []byte("To: bob@hermex.test")) {
		t.Error("To header was dropped")
	}
	if !bytes.Contains(out, []byte("body")) {
		t.Error("body was dropped")
	}
}

// TestRecipientsOf confirms To, Cc, and Bcc addresses are all collected.
func TestRecipientsOf(t *testing.T) {
	raw := []byte("To: bob@hermex.test\r\nCc: carol@hermex.test\r\nBcc: dan@hermex.test\r\nSubject: x\r\n\r\nbody")
	got := recipientsOf(raw)
	want := map[string]bool{"bob@hermex.test": true, "carol@hermex.test": true, "dan@hermex.test": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want 3 recipients", got)
	}
	for _, a := range got {
		if !want[a] {
			t.Errorf("unexpected recipient %q", a)
		}
	}
}
