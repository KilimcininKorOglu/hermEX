package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// readDetail seeds one message into the inbox and returns the detail JSON the
// reader receives for it.
func readDetail(t *testing.T, raw string) map[string]any {
	t.Helper()
	mbox := t.TempDir()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1700000000, 0), 0)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()

	secret := []byte("body-type-test-secret")
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/message?id="+messageID("inbox", info.UID), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET message = %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return out
}

// A text/plain body is not markup. The reader renders the body in an HTML sink,
// so it has to be told the body is text; without that the sender's literal
// "<b>bold</b>" is displayed bold and "&lt;" is displayed as "<".
func TestAPlainBodyIsReportedAsText(t *testing.T) {
	raw := "From: bob@hermex.test\r\n" +
		"Subject: plain\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"<b>bold</b> and &lt;test&gt; and a < b\r\n"
	d := readDetail(t, raw)
	if got := d["bodyType"]; got != "text" {
		t.Fatalf("bodyType = %v, want text", got)
	}
	body, _ := d["body"].(string)
	if body == "" {
		t.Fatal("body is empty")
	}
	// The body stays the message's own bytes; escaping is the reader's step, so a
	// search or an export still sees what the sender wrote.
	if want := "<b>bold</b> and &lt;test&gt; and a < b"; !strings.Contains(body, want) {
		t.Fatalf("body = %q, want it to carry %q verbatim", body, want)
	}
}

// An HTML body is markup and must not be escaped, or every message would display
// its own tags.
func TestAnHTMLBodyIsReportedAsHTML(t *testing.T) {
	raw := "From: bob@hermex.test\r\n" +
		"Subject: rich\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<p><b>bold</b></p>\r\n"
	d := readDetail(t, raw)
	if got := d["bodyType"]; got != "html" {
		t.Fatalf("bodyType = %v, want html", got)
	}
}
