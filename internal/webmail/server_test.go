package webmail

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/store"
)

func seedMailbox(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alice.sqlite3")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	inbox, err := st.CreateFolder(nil, inboxName)
	if err != nil {
		t.Fatal(err)
	}
	msg := "From: Bob <bob@example.com>\r\nSubject: hello webmail\r\n\r\nbody"
	if _, err := st.AppendMessage(inbox, []byte(msg), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestServer(t *testing.T, path string) *httptest.Server {
	t.Helper()
	auth := directory.StaticAccounts{"alice@hermex.test": {Password: "secret", MailboxPath: path}}
	srv, err := NewServer(auth, auth, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// authedClient returns an http.Client whose cookie jar holds a valid session.
func authedClient(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.PostForm(ts.URL+"/login", url.Values{"user": {"alice@hermex.test"}, "password": {"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return c
}

func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestWebmailLoginAndList(t *testing.T) {
	ts := newTestServer(t, seedMailbox(t))
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	// The login page renders.
	if code, body := get(t, c, ts.URL+"/login"); code != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("GET /login = %d, body has Sign in? %v", code, strings.Contains(body, "Sign in"))
	}

	// Unauthenticated /mail redirects to /login (the client follows it).
	if code, body := get(t, c, ts.URL+"/mail"); code != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("unauth /mail did not land on login: %d", code)
	}

	// Wrong credentials are rejected.
	resp, err := c.PostForm(ts.URL+"/login", url.Values{"user": {"alice@hermex.test"}, "password": {"nope"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401", resp.StatusCode)
	}

	// Correct credentials set a session and land on the mailbox (the client
	// follows the redirect to /mail).
	resp, err = c.PostForm(ts.URL+"/login", url.Values{"user": {"alice@hermex.test"}, "password": {"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("post login final status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), inboxName) {
		t.Errorf("mail page missing INBOX folder")
	}
	if !strings.Contains(string(body), "hello webmail") {
		t.Errorf("mail page missing message subject")
	}
	if !strings.Contains(string(body), "Bob") {
		t.Errorf("mail page missing sender")
	}

	// The session persists, so /mail is now reachable directly.
	if code, mailBody := get(t, c, ts.URL+"/mail"); code != 200 || !strings.Contains(mailBody, "hello webmail") {
		t.Fatalf("authed /mail = %d", code)
	}

	// Logout clears the session; /mail bounces back to login.
	if code, _ := get(t, c, ts.URL+"/logout"); code != 200 {
		t.Fatalf("logout = %d", code)
	}
	if code, body := get(t, c, ts.URL+"/mail"); code != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("post-logout /mail did not return to login: %d", code)
	}
}

func TestWebmailReadMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice.sqlite3")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := st.CreateFolder(nil, inboxName)
	if err != nil {
		t.Fatal(err)
	}
	plain := "From: A <a@example.com>\r\nTo: alice@hermex.test\r\nSubject: plain hello\r\n\r\nThis is plain text.\r\n"
	if _, err := st.AppendMessage(inbox, []byte(plain), time.Unix(1, 0), 0); err != nil {
		t.Fatal(err)
	}
	multipart := "From: B <b@example.com>\r\nTo: alice@hermex.test\r\nSubject: with attachment\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"X\"\r\n\r\n" +
		"--X\r\nContent-Type: text/plain\r\n\r\nSee attached.\r\n" +
		"--X\r\nContent-Type: application/octet-stream; name=\"data.bin\"\r\n" +
		"Content-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"data.bin\"\r\n\r\n" +
		"SGVsbG8=\r\n--X--\r\n"
	if _, err := st.AppendMessage(inbox, []byte(multipart), time.Unix(2, 0), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// Plain message: body and headers render.
	code, body := get(t, c, ts.URL+"/message?folder=INBOX&uid=1")
	if code != 200 || !strings.Contains(body, "This is plain text.") {
		t.Fatalf("read plain = %d, body? %v", code, strings.Contains(body, "This is plain text."))
	}
	if !strings.Contains(body, "plain hello") || !strings.Contains(body, "a@example.com") {
		t.Errorf("plain message headers missing")
	}

	// Reading marked it \Seen in the store.
	st2, _ := store.Open(path)
	flags, _ := st2.MessageFlags(inbox, 1)
	st2.Close()
	if flags&store.FlagSeen == 0 {
		t.Errorf("reading did not set \\Seen (flags=%d)", flags)
	}

	// Multipart message: text body plus an attachment with a download link.
	code, body = get(t, c, ts.URL+"/message?folder=INBOX&uid=2")
	if code != 200 || !strings.Contains(body, "See attached.") {
		t.Fatalf("read multipart = %d", code)
	}
	if !strings.Contains(body, "data.bin") {
		t.Errorf("attachment filename missing: %s", body)
	}
	if !strings.Contains(body, "uid=2&amp;part=2") {
		t.Errorf("attachment download link (part=2) missing")
	}
}

func TestWebmailAttachmentDownload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice.sqlite3")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := st.CreateFolder(nil, inboxName)
	if err != nil {
		t.Fatal(err)
	}
	msg := "From: B <b@example.com>\r\nSubject: with attachment\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"X\"\r\n\r\n" +
		"--X\r\nContent-Type: text/plain\r\n\r\nSee attached.\r\n" +
		"--X\r\nContent-Type: application/octet-stream; name=\"data.bin\"\r\n" +
		"Content-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"data.bin\"\r\n\r\n" +
		"SGVsbG8=\r\n--X--\r\n"
	if _, err := st.AppendMessage(inbox, []byte(msg), time.Unix(1, 0), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	resp, err := c.Get(ts.URL + "/attachment?folder=INBOX&uid=1&part=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("attachment status = %d", resp.StatusCode)
	}
	// The base64 part decodes to "Hello".
	if string(body) != "Hello" {
		t.Errorf("attachment body = %q, want Hello", body)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "data.bin") {
		t.Errorf("Content-Disposition = %q, want filename data.bin", cd)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestWebmailCompose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice.sqlite3")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close() // start empty; delivery creates INBOX and Sent

	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// Compose to alice herself (a local recipient resolving to this store).
	resp, err := c.PostForm(ts.URL+"/compose", url.Values{
		"to":      {"alice@hermex.test"},
		"subject": {"Hi there"},
		"body":    {"Hello from webmail"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("compose status = %d", resp.StatusCode)
	}
	// The redirect lands on the Sent folder listing the new message.
	if !strings.Contains(string(body), "Hi there") {
		t.Errorf("Sent listing missing the composed message")
	}

	// The message was delivered to the local INBOX and copied to Sent.
	st2, _ := store.Open(path)
	defer st2.Close()
	inbox, ok, _ := st2.FolderByName(nil, "INBOX")
	if !ok {
		t.Fatal("INBOX not created by delivery")
	}
	if msgs, _ := st2.ListMessages(inbox); len(msgs) != 1 {
		t.Errorf("INBOX has %d messages, want 1", len(msgs))
	}
	sent, ok, _ := st2.FolderByName(nil, "Sent")
	if !ok {
		t.Fatal("Sent not created")
	}
	smsgs, _ := st2.ListMessages(sent)
	if len(smsgs) != 1 {
		t.Fatalf("Sent has %d messages, want 1", len(smsgs))
	}
	raw, _ := st2.GetMessageRaw(sent, smsgs[0].UID)
	if !strings.Contains(string(raw), "Hello from webmail") || !strings.Contains(string(raw), "Subject: Hi there") {
		t.Errorf("Sent copy content unexpected: %s", raw)
	}

	// A non-local recipient is reported (no relay yet), not silently dropped.
	resp, err = c.PostForm(ts.URL+"/compose", url.Values{
		"to":      {"nobody@external.invalid"},
		"subject": {"x"},
		"body":    {"y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	nbody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(nbody), "nobody@external.invalid") {
		t.Errorf("unresolved recipient not surfaced: %s", nbody)
	}
}

func postAction(t *testing.T, c *http.Client, ts *httptest.Server, query string) (int, string) {
	t.Helper()
	resp, err := c.Post(ts.URL+"/action?"+query, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestWebmailActions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice.sqlite3")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := st.CreateFolder(nil, inboxName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(inbox, []byte("From: a@example.com\r\nSubject: act\r\n\r\nx"), time.Unix(1, 0), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// Toggle \Seen: the returned row partial flips to offering "Unread".
	code, body := postAction(t, c, ts, "folder=INBOX&uid=1&op=toggleseen")
	if code != 200 || !strings.Contains(body, `id="msg-1"`) || !strings.Contains(body, "Unread") {
		t.Fatalf("toggleseen row = %d %q", code, body)
	}
	// Toggle \Flagged: the row gains the flagged class.
	if _, body := postAction(t, c, ts, "folder=INBOX&uid=1&op=toggleflag"); !strings.Contains(body, "flagged") {
		t.Errorf("toggleflag row missing flagged class: %s", body)
	}

	st2, _ := store.Open(path)
	if f, _ := st2.MessageFlags(inbox, 1); f&store.FlagSeen == 0 || f&store.FlagFlagged == 0 {
		t.Errorf("flags not persisted: %d", f)
	}
	st2.Close()

	// Delete moves the message to Trash (empty body; htmx removes the row).
	if code, body := postAction(t, c, ts, "folder=INBOX&uid=1&op=delete"); code != 200 || strings.TrimSpace(body) != "" {
		t.Errorf("delete = %d %q", code, body)
	}
	st3, _ := store.Open(path)
	defer st3.Close()
	if msgs, _ := st3.ListMessages(inbox); len(msgs) != 0 {
		t.Errorf("INBOX still has %d messages after delete", len(msgs))
	}
	trash, ok, _ := st3.FolderByName(nil, "Trash")
	if !ok {
		t.Fatal("Trash not created on delete")
	}
	if msgs, _ := st3.ListMessages(trash); len(msgs) != 1 {
		t.Errorf("Trash has %d messages, want 1", len(msgs))
	}
}
