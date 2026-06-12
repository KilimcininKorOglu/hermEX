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
	srv, err := NewServer(auth, "mail.test")
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
