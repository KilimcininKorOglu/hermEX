package webmail

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// seedRaw files a raw RFC822 message into a folder and returns its UID. Unlike
// seedMsg (which always builds a text/plain body) it takes the full raw bytes,
// so a test can seed an HTML-only or multipart/alternative message.
func seedRaw(t *testing.T, path string, fid int64, raw string) uint32 {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	info, err := st.AppendMessage(fid, []byte(raw), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.UID
}

const htmlOnlyMsg = "From: sender@example.com\r\n" +
	"To: alice@hermex.test\r\n" +
	"Subject: html only\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<html><body><p>Hello <b>bold</b> world</p><div>second&nbsp;line</div>" +
	"<script>alert('x')</script></body></html>"

const altMsg = "From: sender@example.com\r\n" +
	"To: alice@hermex.test\r\n" +
	"Subject: alternative\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=\"BND\"\r\n" +
	"\r\n--BND\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
	"PLAIN ALTERNATIVE BODY\r\n" +
	"--BND\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n\r\n" +
	"<p>HTML ALTERNATIVE BODY</p>\r\n" +
	"--BND--\r\n"

// TestHTMLToText checks the down-converter: scripts are dropped, block
// boundaries become newlines, tags are removed, and entities are unescaped —
// never leaving raw markup behind.
func TestHTMLToText(t *testing.T) {
	got := htmlToText("<p>Hello <b>world</b></p><br>line2<script>alert('x')</script>&amp; done")
	if strings.ContainsAny(got, "<>") {
		t.Errorf("down-converted text still contains tag characters: %q", got)
	}
	for _, want := range []string{"Hello world", "line2", "& done"} {
		if !strings.Contains(got, want) {
			t.Errorf("down-converted text missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "alert") {
		t.Errorf("script content leaked into text: %q", got)
	}
}

// TestForcePlainOnHTMLOnly verifies that an HTML-only message is shown as HTML
// by default but down-converted to tag-free text when plain is forced (the
// advisor's "don't dump raw <div> tags" case).
func TestForcePlainOnHTMLOnly(t *testing.T) {
	html := buildMessageDetail([]byte(htmlOnlyMsg), "INBOX", 1, false)
	if !html.IsHTML {
		t.Fatalf("default render of an HTML message should be HTML, got plain: %q", html.Body)
	}

	plain := buildMessageDetail([]byte(htmlOnlyMsg), "INBOX", 1, true)
	if plain.IsHTML {
		t.Fatalf("forced-plain render should not be HTML")
	}
	if strings.ContainsAny(plain.Body, "<>") {
		t.Errorf("forced-plain render leaked raw markup: %q", plain.Body)
	}
	if !strings.Contains(plain.Body, "Hello bold world") {
		t.Errorf("forced-plain render lost the text content: %q", plain.Body)
	}
	// &nbsp; is unescaped (to U+00A0), so the literal entity must be gone while
	// both words survive.
	if strings.Contains(plain.Body, "&nbsp;") || !strings.Contains(plain.Body, "second") || !strings.Contains(plain.Body, "line") {
		t.Errorf("forced-plain render did not unescape the entity: %q", plain.Body)
	}
}

// TestForcePlainPrefersPlainAlternative verifies that for a multipart/alternative
// message the plain part is chosen verbatim (no down-convert needed) when plain
// is forced, and the HTML part is chosen by default.
func TestForcePlainPrefersPlainAlternative(t *testing.T) {
	def := buildMessageDetail([]byte(altMsg), "INBOX", 1, false)
	if !def.IsHTML || !strings.Contains(def.Body, "HTML ALTERNATIVE BODY") {
		t.Errorf("default should pick the HTML alternative, got isHTML=%v body=%q", def.IsHTML, def.Body)
	}

	plain := buildMessageDetail([]byte(altMsg), "INBOX", 1, true)
	if plain.IsHTML {
		t.Errorf("forced-plain should pick the text alternative, got HTML")
	}
	if !strings.Contains(plain.Body, "PLAIN ALTERNATIVE BODY") {
		t.Errorf("forced-plain did not pick the text/plain alternative: %q", plain.Body)
	}
	if strings.Contains(plain.Body, "HTML ALTERNATIVE BODY") {
		t.Errorf("forced-plain leaked the HTML alternative: %q", plain.Body)
	}
}

// TestIncomingRenderFlowsToReader locks the end-to-end wiring: with the default
// "html" setting the reader serves the HTML iframe; after saving "plain" it
// serves the plain-text block instead, for the same HTML-only message.
func TestIncomingRenderFlowsToReader(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedRaw(t, path, int64(mapi.PrivateFIDInbox), htmlOnlyMsg)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, def := get(t, c, ts.URL+"/message?folder=INBOX&uid="+itoa(uid))
	if !strings.Contains(def, `class="htmlbody"`) {
		t.Fatalf("default reader did not render the HTML iframe:\n%s", def)
	}

	if code, _ := postForm(t, c, ts.URL+"/settings", url.Values{
		"action": {"save"}, "incomingrender": {"plain"},
	}); code != 200 && code != 303 {
		t.Fatalf("saving incomingrender=plain = %d", code)
	}

	_, plain := get(t, c, ts.URL+"/message?folder=INBOX&uid="+itoa(uid))
	if strings.Contains(plain, `class="htmlbody"`) {
		t.Errorf("forced-plain reader still rendered the HTML iframe:\n%s", plain)
	}
	if !strings.Contains(plain, `class="plainbody"`) {
		t.Errorf("forced-plain reader did not render the plain-text block:\n%s", plain)
	}
}

// TestRequestReceiptDefaultCompose locks the compose default: off by default the
// box is unchecked; after enabling it, a fresh compose and a reply pre-check it,
// while reopening a draft preserves the draft's own state (no forced default).
func TestRequestReceiptDefaultCompose(t *testing.T) {
	path := emptyMailbox(t)
	src := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "hi", "alice@hermex.test", "body", 1700000000, 0)
	draft := seedMsg(t, path, draftFID, "wip", "bob@hermex.test", "draft body", 1700000100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	const checked = `name="readreceipt" value="1" checked`

	_, fresh := get(t, c, ts.URL+"/compose")
	if strings.Contains(fresh, checked) {
		t.Errorf("read-receipt box pre-checked with the default off:\n%s", fresh)
	}

	if code, _ := postForm(t, c, ts.URL+"/settings", url.Values{
		"action": {"save"}, "requestreceipt": {"1"},
	}); code != 200 && code != 303 {
		t.Fatalf("saving requestreceipt=1 = %d", code)
	}

	_, fresh2 := get(t, c, ts.URL+"/compose")
	if !strings.Contains(fresh2, checked) {
		t.Errorf("read-receipt box not pre-checked after enabling the default:\n%s", fresh2)
	}

	_, reply := get(t, c, ts.URL+"/compose?action=reply&folder=INBOX&uid="+itoa(src))
	if !strings.Contains(reply, checked) {
		t.Errorf("reply did not honor the read-receipt default:\n%s", reply)
	}

	_, edit := get(t, c, ts.URL+"/compose?action=editdraft&folder=Drafts&uid="+itoa(draft))
	if strings.Contains(edit, checked) {
		t.Errorf("editdraft forced the read-receipt default over the draft's own state:\n%s", edit)
	}
}

// TestSettingsRoundTripReceiptAndRender checks that the two new preferences
// persist and are reflected back in the settings form, and that clearing the
// checkbox on a later save turns the default back off.
func TestSettingsRoundTripReceiptAndRender(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code, _ := postForm(t, c, ts.URL+"/settings", url.Values{
		"action": {"save"}, "incomingrender": {"plain"}, "requestreceipt": {"1"},
	}); code != 200 && code != 303 {
		t.Fatalf("save = %d", code)
	}
	_, form := get(t, c, ts.URL+"/settings")
	if !strings.Contains(form, `name="incomingrender" value="plain" checked`) {
		t.Errorf("settings form did not reflect incomingrender=plain:\n%s", form)
	}
	if !strings.Contains(form, `name="requestreceipt" value="1" checked`) {
		t.Errorf("settings form did not reflect requestreceipt on:\n%s", form)
	}

	// A later save without the checkbox clears it.
	if code, _ := postForm(t, c, ts.URL+"/settings", url.Values{
		"action": {"save"}, "incomingrender": {"html"},
	}); code != 200 && code != 303 {
		t.Fatalf("second save = %d", code)
	}
	_, form2 := get(t, c, ts.URL+"/settings")
	if strings.Contains(form2, `name="requestreceipt" value="1" checked`) {
		t.Errorf("clearing the checkbox did not turn the default off:\n%s", form2)
	}
}
