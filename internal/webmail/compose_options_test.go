package webmail

import (
	"io"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/store"
)

// folderRaw returns the raw bytes of the single message in folder of the store
// at path.
func folderRaw(t *testing.T, path, folder string) string {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f, ok, err := st.FolderByName(nil, folder)
	if err != nil || !ok {
		t.Fatalf("%s missing in %s (ok=%v err=%v)", folder, path, ok, err)
	}
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
	path := filepath.Join(t.TempDir(), "alice.sqlite3")
	st, _ := store.Open(path)
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
	for _, want := range []string{
		"X-Priority: 1",
		"Importance: high",
		"Sensitivity: Private",
		"Disposition-Notification-To: alice@hermex.test",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("Sent copy missing %q:\n%s", want, raw)
		}
	}
}

func TestWebmailBccNotLeaked(t *testing.T) {
	dir := t.TempDir()
	alicePath := filepath.Join(dir, "alice.sqlite3")
	bobPath := filepath.Join(dir, "bob.sqlite3")
	for _, p := range []string{alicePath, bobPath} {
		st, err := store.Open(p)
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
	// The sender's Sent copy DOES record the Bcc.
	if got := folderRaw(t, alicePath, "Sent"); !strings.Contains(got, "Bcc: bob@hermex.test") {
		t.Errorf("Sent copy missing Bcc record:\n%s", got)
	}
}

func TestWebmailFromGatingFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice.sqlite3")
	st, _ := store.Open(path)
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
	if !strings.Contains(raw, "From: alice@hermex.test") {
		t.Errorf("From not gated to session user:\n%s", raw)
	}
	if strings.Contains(raw, "ceo@victim.test") {
		t.Errorf("spoofed From leaked into the message:\n%s", raw)
	}
}
