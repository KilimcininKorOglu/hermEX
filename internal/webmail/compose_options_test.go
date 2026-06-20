package webmail

import (
	"io"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// wellKnownFID maps the well-known folder names the tests use to their built-in
// folder ids.
func wellKnownFID(t *testing.T, name string) int64 {
	t.Helper()
	switch name {
	case "INBOX":
		return int64(mapi.PrivateFIDInbox)
	case "Sent":
		return int64(mapi.PrivateFIDSentItems)
	case "Trash":
		return int64(mapi.PrivateFIDDeletedItems)
	default:
		t.Fatalf("unknown well-known folder %q", name)
		return 0
	}
}

// headerValue returns the value of the named header from a re-synthesized
// message, matched on a single line within the header block. It lets a test
// assert that a specific header carries an expected address without coupling to
// the exact display-name form the export path synthesizes (a nameless address
// round-trips as `"addr" <addr>`, the reference-faithful form).
func headerValue(raw, name string) string {
	prefix := name + ":"
	for line := range strings.SplitSeq(raw, "\r\n") {
		if line == "" {
			break // blank line ends the header block
		}
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

// folderRaw returns the raw bytes of the single message in folder of the store
// at path.
func folderRaw(t *testing.T, path, folder string) string {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f := wellKnownFID(t, folder)
	msgs, _ := st.ListMessages(f)
	if len(msgs) != 1 {
		t.Fatalf("%s has %d messages, want 1", folder, len(msgs))
	}
	raw, err := st.GetMessageRaw(f, msgs[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestWebmailComposeHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	st, _ := objectstore.Open(path)
	st.Close()
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	resp, err := c.PostForm(ts.URL+"/compose", url.Values{
		"to":          {"alice@hermex.test"},
		"subject":     {"Flagged"},
		"body":        {"hi"},
		"importance":  {"high"},
		"sensitivity": {"private"},
		"readreceipt": {"1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	raw := folderRaw(t, path, "Sent")
	// The composed options survive the parse/re-synthesize round trip as their
	// MAPI-derived headers. Importance uses the capitalized export form.
	for _, want := range []string{"Importance: High", "Sensitivity: Private"} {
		if !strings.Contains(raw, want) {
			t.Errorf("Sent copy missing %q:\n%s", want, raw)
		}
	}
	// The priority is conveyed by the Importance header alone; the export path
	// does not also emit the redundant X-Priority.
	if strings.Contains(raw, "X-Priority") {
		t.Errorf("export should not emit X-Priority (Importance carries it):\n%s", raw)
	}
	// The read-receipt request re-emits Disposition-Notification-To addressed to
	// the sender (the header is absent unless the request was honored).
	if dnt := headerValue(raw, "Disposition-Notification-To"); !strings.Contains(dnt, "alice@hermex.test") {
		t.Errorf("read-receipt request not recorded (Disposition-Notification-To = %q):\n%s", dnt, raw)
	}
}

func TestWebmailBccNotLeaked(t *testing.T) {
	dir := t.TempDir()
	alicePath := filepath.Join(dir, "alice")
	bobPath := filepath.Join(dir, "bob")
	for _, p := range []string{alicePath, bobPath} {
		st, err := objectstore.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	accts := directory.StaticAccounts{
		"alice@hermex.test": {Password: "secret", MailboxPath: alicePath},
		"bob@hermex.test":   {Password: "secret", MailboxPath: bobPath},
	}
	srv, err := NewServer(accts, accts, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	c := authedClient(t, ts)

	// alice sends to herself, blind-copying bob.
	resp, err := c.PostForm(ts.URL+"/compose", url.Values{
		"to":      {"alice@hermex.test"},
		"bcc":     {"bob@hermex.test"},
		"subject": {"Secret"},
		"body":    {"hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	// The delivered copies (To recipient and Bcc recipient) must NOT carry Bcc.
	if got := folderRaw(t, alicePath, "INBOX"); strings.Contains(got, "Bcc:") {
		t.Errorf("To recipient's delivered copy leaks Bcc:\n%s", got)
	}
	if got := folderRaw(t, bobPath, "INBOX"); strings.Contains(got, "Bcc:") {
		t.Errorf("Bcc recipient's delivered copy leaks Bcc:\n%s", got)
	}
	// The sender's Sent copy DOES record the Bcc (the address round-trips in the
	// reference-faithful `"addr" <addr>` display-name form).
	if bcc := headerValue(folderRaw(t, alicePath, "Sent"), "Bcc"); !strings.Contains(bcc, "bob@hermex.test") {
		t.Errorf("Sent copy missing Bcc record (Bcc = %q)", bcc)
	}
}

func TestWebmailFromGatingFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	st, _ := objectstore.Open(path)
	st.Close()
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// A From the user is not permitted to send as must be rejected: the Sent
	// copy is rewritten to the session user, never the spoofed value.
	resp, err := c.PostForm(ts.URL+"/compose", url.Values{
		"from":    {"ceo@victim.test"},
		"to":      {"alice@hermex.test"},
		"subject": {"spoof"},
		"body":    {"hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	raw := folderRaw(t, path, "Sent")
	if from := headerValue(raw, "From"); !strings.Contains(from, "alice@hermex.test") {
		t.Errorf("From not gated to session user (From = %q):\n%s", from, raw)
	}
	if strings.Contains(raw, "ceo@victim.test") {
		t.Errorf("spoofed From leaked into the message:\n%s", raw)
	}
}
