package webmail

import (
	"net/url"
	"strings"
	"testing"
)

// remoteImgMsg is an HTML message from news@example.com carrying a remote image
// (a tracking-pixel shape) — the reader must not load it unless the sender is a
// safe sender.
const remoteImgMsg = "From: News <news@example.com>\r\n" +
	"To: alice@hermex.test\r\n" +
	"Subject: promo\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	`<p>hello</p><img src="http://tracker.example.net/p.gif" width="1" height="1">`

const plainTextMsg = "From: x@example.com\r\n" +
	"To: alice@hermex.test\r\n" +
	"Subject: plain\r\n" +
	"\r\n" +
	"just text, no markup"

// TestIsSafeSender covers the matching rules: exact address, domain (bare and
// @-prefixed), case-insensitivity, and the deliberate non-matches (different
// user, subdomain, empty).
func TestIsSafeSender(t *testing.T) {
	cases := []struct {
		list []string
		addr string
		want bool
	}{
		{[]string{"news@example.com"}, "news@example.com", true},
		{[]string{"News@Example.com"}, "news@example.com", true}, // case-insensitive
		{[]string{"example.com"}, "news@example.com", true},      // bare domain
		{[]string{"@example.com"}, "news@example.com", true},     // @-prefixed domain
		{[]string{"news@example.com"}, "other@example.com", false},
		{[]string{"example.com"}, "user@sub.example.com", false}, // no subdomain match
		{[]string{"example.com"}, "", false},
		{nil, "news@example.com", false},
	}
	for _, c := range cases {
		if got := isSafeSender(c.list, c.addr); got != c.want {
			t.Errorf("isSafeSender(%q, %q) = %v, want %v", c.list, c.addr, got, c.want)
		}
	}
}

// TestRemoteContentBlockedByDefault verifies that an HTML body from a sender who
// is not allow-listed gets the restrictive CSP meta, which permits only data:
// images (no remote subresources).
func TestRemoteContentBlockedByDefault(t *testing.T) {
	d := buildMessageDetail([]byte(remoteImgMsg), "INBOX", 1, false, nil)
	if !d.IsHTML {
		t.Fatalf("expected an HTML body")
	}
	if !strings.Contains(d.Body, "Content-Security-Policy") {
		t.Fatalf("no CSP meta injected:\n%s", d.Body)
	}
	if !strings.Contains(d.Body, "img-src data:") || strings.Contains(d.Body, "img-src * data:") {
		t.Errorf("default CSP is not the restrictive (data:-only) policy:\n%s", d.Body)
	}
	if !strings.Contains(d.Body, "default-src 'none'") {
		t.Errorf("CSP does not forbid scripts/objects/frames via default-src 'none':\n%s", d.Body)
	}
}

// TestRemoteContentAllowedForSafeSender verifies that an allow-listed sender —
// matched either by full address or by domain — gets the permissive CSP that
// also allows remote images.
func TestRemoteContentAllowedForSafeSender(t *testing.T) {
	for _, list := range [][]string{{"news@example.com"}, {"example.com"}} {
		d := buildMessageDetail([]byte(remoteImgMsg), "INBOX", 1, false, list)
		if !strings.Contains(d.Body, "img-src * data:") {
			t.Errorf("safe sender %v did not get the remote-allowing CSP:\n%s", list, d.Body)
		}
	}
}

// TestRemoteContentMetaOnlyForHTML verifies the CSP meta is injected only for an
// HTML body — never for a plain-text message nor a forced-plain down-convert
// (those render in a <pre>, not the iframe, so a meta would be meaningless text).
func TestRemoteContentMetaOnlyForHTML(t *testing.T) {
	plain := buildMessageDetail([]byte(plainTextMsg), "INBOX", 1, false, nil)
	if plain.IsHTML {
		t.Fatalf("plain message should not be HTML")
	}
	if strings.Contains(plain.Body, "Content-Security-Policy") {
		t.Errorf("CSP meta leaked into a plain-text body:\n%s", plain.Body)
	}

	forced := buildMessageDetail([]byte(remoteImgMsg), "INBOX", 1, true, nil)
	if forced.IsHTML {
		t.Fatalf("forced-plain render should not be HTML")
	}
	if strings.Contains(forced.Body, "Content-Security-Policy") {
		t.Errorf("CSP meta leaked into a forced-plain body:\n%s", forced.Body)
	}
}

// TestSafeSendersRoundTrip checks that a safe sender added through the settings
// form is listed and can be removed.
func TestSafeSendersRoundTrip(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code, _ := postForm(t, c, ts.URL+"/settings", url.Values{
		"action": {"addsafe"}, "safesender": {"News@Example.com"},
	}); code != 200 && code != 303 {
		t.Fatalf("addsafe = %d", code)
	}
	_, form := get(t, c, ts.URL+"/settings")
	if !strings.Contains(form, "news@example.com") { // stored lowercased
		t.Errorf("safe sender not listed after add:\n%s", form)
	}

	if code, _ := postForm(t, c, ts.URL+"/settings", url.Values{
		"action": {"delsafe"}, "safesender": {"news@example.com"},
	}); code != 200 && code != 303 {
		t.Fatalf("delsafe = %d", code)
	}
	_, form2 := get(t, c, ts.URL+"/settings")
	if strings.Contains(form2, ">news@example.com<") {
		t.Errorf("safe sender still listed after delete:\n%s", form2)
	}
}
