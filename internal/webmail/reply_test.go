package webmail

import (
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

func TestEnsureSubjectPrefix(t *testing.T) {
	cases := []struct{ prefix, in, want string }{
		{"Re:", "Project update", "Re: Project update"},
		{"Re:", "Re: Project update", "Re: Project update"}, // no double prefix
		{"Re:", "re: lowercase", "re: lowercase"},           // case-insensitive match
		{"Fwd:", "Project update", "Fwd: Project update"},
		{"Re:", "", "Re:"},
	}
	for _, c := range cases {
		if got := ensureSubjectPrefix(c.prefix, c.in); got != c.want {
			t.Errorf("ensureSubjectPrefix(%q,%q) = %q, want %q", c.prefix, c.in, got, c.want)
		}
	}
}

func TestReplyAllCc(t *testing.T) {
	env := &mime.Envelope{
		From:    []mime.Address{{Mailbox: "bob", Host: "example.com"}},
		ReplyTo: []mime.Address{{Mailbox: "bob", Host: "example.com"}}, // ParseEnvelope defaults ReplyTo to From
		To:      []mime.Address{{Mailbox: "alice", Host: "hermex.test"}, {Name: "Carol", Mailbox: "carol", Host: "example.com"}},
		Cc:      []mime.Address{{Mailbox: "dave", Host: "example.com"}},
	}
	cc := replyAllCc(env, "alice@hermex.test")
	got := make([]string, len(cc))
	for i, a := range cc {
		got[i] = addrKey(a)
	}
	want := []string{"carol@example.com", "dave@example.com"} // self (alice) and reply-to (bob) excluded
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("replyAllCc = %v, want %v", got, want)
	}
}

// seedReplyMailbox stores one rich message (From/To/Cc/Message-ID/References)
// and returns the mailbox path.
func seedReplyMailbox(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	inbox := int64(mapi.PrivateFIDInbox)
	msg := "From: Bob <bob@example.com>\r\n" +
		"To: alice@hermex.test, Carol <carol@example.com>\r\n" +
		"Cc: dave@example.com\r\n" +
		"Subject: Project update\r\n" +
		"Message-ID: <orig-123@example.com>\r\n" +
		"References: <root-1@example.com>\r\n" +
		"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\n" +
		"\r\n" +
		"Original body line 1\r\nOriginal body line 2\r\n"
	if _, err := st.AppendMessage(inbox, []byte(msg), time.Unix(1, 0), 0); err != nil {
		t.Fatal(err)
	}
	return path
}

// formFieldValue extracts the value attribute of the named input from rendered
// compose HTML. The attribute is HTML-escaped, so it contains no raw double
// quote and ends at the next one.
func formFieldValue(html, name string) string {
	marker := `name="` + name + `" value="`
	_, after, ok := strings.Cut(html, marker)
	if !ok {
		return ""
	}
	rest := after
	if before, _, ok := strings.Cut(rest, "\""); ok {
		return before
	}
	return rest
}

func TestWebmailReplyPrefill(t *testing.T) {
	ts := newTestServer(t, seedReplyMailbox(t))
	c := authedClient(t, ts)

	// Reply: To = original sender, Re: subject, quoted body + attribution.
	code, body := get(t, c, ts.URL+"/compose?action=reply&folder=INBOX&uid=1")
	if code != 200 {
		t.Fatalf("reply prefill = %d", code)
	}
	if !strings.Contains(body, `value="Re: Project update"`) {
		t.Errorf("reply subject not prefixed: %s", body)
	}
	if !strings.Contains(body, `value="Bob &lt;bob@example.com&gt;"`) {
		t.Errorf("reply To is not the original sender: %s", body)
	}
	if !strings.Contains(body, "Bob wrote:") || !strings.Contains(body, "&gt; Original body line 1") {
		t.Errorf("reply body missing attribution/quote: %s", body)
	}
	// Threading hidden fields are prefilled from the source (RFC 5322 §3.6.4):
	// In-Reply-To = parent Message-ID, References = parent References + Message-ID.
	if !strings.Contains(body, `name="inreplyto" value="&lt;orig-123@example.com&gt;"`) {
		t.Errorf("reply In-Reply-To not prefilled: %s", body)
	}
	if !strings.Contains(body, `name="references" value="&lt;root-1@example.com&gt; &lt;orig-123@example.com&gt;"`) {
		t.Errorf("reply References not prefilled: %s", body)
	}

	// Reply All: Cc carries original To+Cc minus self (alice) and minus sender (bob).
	code, body = get(t, c, ts.URL+"/compose?action=replyall&folder=INBOX&uid=1")
	if code != 200 {
		t.Fatalf("replyall prefill = %d", code)
	}
	// Cc carries the original To+Cc minus self (alice) and minus the sender
	// (bob, already the reply target in To). carol keeps its display name; dave,
	// nameless, round-trips in the reference-faithful `"addr" <addr>` form — so
	// assert the addresses are present/absent rather than the exact display form.
	cc := formFieldValue(body, "cc")
	for _, want := range []string{"carol@example.com", "dave@example.com"} {
		if !strings.Contains(cc, want) {
			t.Errorf("replyall Cc missing %q (cc = %q)", want, cc)
		}
	}
	for _, exclude := range []string{"alice@hermex.test", "bob@example.com"} {
		if strings.Contains(cc, exclude) {
			t.Errorf("replyall Cc must exclude %q (cc = %q)", exclude, cc)
		}
	}

	// Forward: Fwd: subject, forwarded banner, no In-Reply-To linkage.
	code, body = get(t, c, ts.URL+"/compose?action=forward&folder=INBOX&uid=1")
	if code != 200 {
		t.Fatalf("forward prefill = %d", code)
	}
	if !strings.Contains(body, `value="Fwd: Project update"`) {
		t.Errorf("forward subject not prefixed")
	}
	if !strings.Contains(body, "Forwarded message") {
		t.Errorf("forward body missing banner: %s", body)
	}
	if !strings.Contains(body, `name="inreplyto" value=""`) {
		t.Errorf("forward must not set In-Reply-To: %s", body)
	}
}

// sentRaw returns the raw bytes of the single message in the Sent folder.
func sentRaw(t *testing.T, path string) string {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sent := int64(mapi.PrivateFIDSentItems)
	msgs, _ := st.ListMessages(sent)
	if len(msgs) != 1 {
		t.Fatalf("Sent has %d messages, want 1", len(msgs))
	}
	raw, err := st.GetMessageRaw(sent, msgs[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestWebmailReplySendThreadingHeaders(t *testing.T) {
	path := seedReplyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// Post a reply carrying the threading headers the prefill produced.
	resp, err := c.PostForm(ts.URL+"/compose", url.Values{
		"to":         {"alice@hermex.test"}, // local, so it also delivers
		"cc":         {"carol@example.com"},
		"subject":    {"Re: Project update"},
		"body":       {"My reply"},
		"inreplyto":  {"<orig-123@example.com>"},
		"references": {"<root-1@example.com> <orig-123@example.com>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	raw := sentRaw(t, path)
	if !strings.Contains(raw, "In-Reply-To: <orig-123@example.com>") {
		t.Errorf("Sent copy missing In-Reply-To: %s", raw)
	}
	if !strings.Contains(raw, "References: <root-1@example.com> <orig-123@example.com>") {
		t.Errorf("Sent copy missing/!wrong References: %s", raw)
	}
	if cc := headerValue(raw, "Cc"); !strings.Contains(cc, "carol@example.com") {
		t.Errorf("Sent copy missing Cc header (Cc = %q): %s", cc, raw)
	}
}

func TestWebmailForwardAsAttachment(t *testing.T) {
	path := seedReplyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// The forward-as-attachment prefill carries the source folder+uid as hidden
	// fields; the submit embeds the original verbatim as message/rfc822.
	code, body := get(t, c, ts.URL+"/compose?action=forwardasattach&folder=INBOX&uid=1")
	if code != 200 {
		t.Fatalf("forwardasattach prefill = %d", code)
	}
	if !strings.Contains(body, `name="attachfolder" value="INBOX"`) || !strings.Contains(body, `name="attachuid" value="1"`) {
		t.Errorf("forwardasattach hidden source fields missing: %s", body)
	}

	resp, err := c.PostForm(ts.URL+"/compose", url.Values{
		"to":           {"alice@hermex.test"},
		"subject":      {"Fwd: Project update"},
		"body":         {"See attached"},
		"attachfolder": {"INBOX"},
		"attachuid":    {"1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	raw := sentRaw(t, path)
	if !strings.Contains(raw, "multipart/mixed") || !strings.Contains(raw, "message/rfc822") {
		t.Errorf("forward-as-attachment is not multipart/mixed with message/rfc822: %s", raw)
	}
	// The embedded original is present verbatim.
	if !strings.Contains(raw, "Subject: Project update") || !strings.Contains(raw, "Original body line 1") {
		t.Errorf("embedded original message missing: %s", raw)
	}
}
