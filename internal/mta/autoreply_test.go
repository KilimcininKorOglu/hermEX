package mta

import (
	"bytes"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"testing"
	"time"

	"hermex/internal/objectstore"
)

const selfAddr = "me@hermex.test"

// TestAutoReplySuppressed is the safety table for the out-of-office loop break.
// The crucial rows are the two "absent header" cases and the explicit "no":
// suppressing on an absent Auto-Submitted/Precedence would silently kill every
// reply (the feature would never fire), so those must NOT suppress, while a
// present non-"no" keyword must.
func TestAutoReplySuppressed(t *testing.T) {
	cases := []struct {
		name   string
		hdr    mail.Header
		sender string
		want   bool
	}{
		{"ordinary person-to-person", mail.Header{}, "bob@example.com", false},
		{"auto-submitted absent replies", mail.Header{}, "bob@example.com", false},
		{"auto-submitted no replies", mail.Header{"Auto-Submitted": {"no"}}, "bob@example.com", false},
		{"auto-submitted no with param replies", mail.Header{"Auto-Submitted": {"no; param=1"}}, "bob@example.com", false},
		{"auto-submitted auto-replied suppressed", mail.Header{"Auto-Submitted": {"auto-replied"}}, "bob@example.com", true},
		{"auto-submitted uppercase suppressed", mail.Header{"Auto-Submitted": {"AUTO-REPLIED"}}, "bob@example.com", true},
		{"auto-submitted auto-generated suppressed", mail.Header{"Auto-Submitted": {"auto-generated"}}, "bob@example.com", true},
		{"auto-submitted keyword with comment suppressed", mail.Header{"Auto-Submitted": {"auto-generated (rejected)"}}, "bob@example.com", true},
		{"precedence absent replies", mail.Header{}, "bob@example.com", false},
		{"precedence bulk suppressed", mail.Header{"Precedence": {"bulk"}}, "bob@example.com", true},
		{"precedence uppercase bulk suppressed", mail.Header{"Precedence": {"BULK"}}, "bob@example.com", true},
		{"precedence list suppressed", mail.Header{"Precedence": {"list"}}, "bob@example.com", true},
		{"precedence junk suppressed", mail.Header{"Precedence": {"junk"}}, "bob@example.com", true},
		{"precedence other replies", mail.Header{"Precedence": {"first-class"}}, "bob@example.com", false},
		{"list-id suppressed", mail.Header{"List-Id": {"<list.example.com>"}}, "bob@example.com", true},
		{"list-unsubscribe suppressed", mail.Header{"List-Unsubscribe": {"<mailto:x@y>"}}, "bob@example.com", true},
		{"list-post suppressed", mail.Header{"List-Post": {"<mailto:list@example.com>"}}, "bob@example.com", true},
		{"null sender suppressed", mail.Header{}, "", true},
		{"angle null sender suppressed", mail.Header{}, "<>", true},
		{"self suppressed", mail.Header{}, selfAddr, true},
		{"self uppercase suppressed", mail.Header{}, "ME@HERMEX.TEST", true},
		{"self with display name suppressed", mail.Header{}, "Me <" + selfAddr + ">", true},
		{"postmaster suppressed", mail.Header{}, "postmaster@example.com", true},
		{"mailer-daemon suppressed", mail.Header{}, "MAILER-DAEMON@example.com", true},
		{"no-reply suppressed", mail.Header{}, "no-reply@example.com", true},
		{"noreply suppressed", mail.Header{}, "noreply@example.com", true},
		{"do-not-reply suppressed", mail.Header{}, "do-not-reply@example.com", true},
		{"donotreply suppressed", mail.Header{}, "donotreply@example.com", true},
	}
	for _, c := range cases {
		if got := autoReplySuppressed(c.hdr, c.sender, selfAddr); got != c.want {
			t.Errorf("%s: autoReplySuppressed = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestAutoReplyDecision covers subject/body selection and the external-reply
// gate, including the external audience. The external rows are the ones the
// delivery-path tests cannot reach (an external sender has no local mailbox, so
// its reply is dropped), so they are pinned here, in particular that the
// known-only audience withholds the reply from an unknown sender.
func TestAutoReplyDecision(t *testing.T) {
	cfg := objectstore.OOFSettings{
		InternalSubject: "internal subj", InternalReply: "internal text",
		ExternalSubject: "external subj", ExternalReply: "external text",
	}
	cfgExtAll := cfg
	cfgExtAll.ExternalEnabled = true // ExternalAudience defaults to All (0)
	cfgExtKnown := cfg
	cfgExtKnown.ExternalEnabled = true
	cfgExtKnown.ExternalAudience = objectstore.OOFExternalKnown

	cases := []struct {
		name        string
		hdr         mail.Header
		sender      string
		cfg         objectstore.OOFSettings
		internal    bool
		senderKnown bool
		wantSubject string
		wantBody    string
		wantSend    bool
	}{
		{"internal sender gets internal reply", mail.Header{}, "bob@example.com", cfg, true, false, "internal subj", "internal text", true},
		{"external sender with external disabled is silent", mail.Header{}, "bob@example.com", cfg, false, false, "", "", false},
		{"external all replies to an unknown sender", mail.Header{}, "bob@example.com", cfgExtAll, false, false, "external subj", "external text", true},
		{"external known replies to a known sender", mail.Header{}, "bob@example.com", cfgExtKnown, false, true, "external subj", "external text", true},
		{"external known is silent to an unknown sender", mail.Header{}, "bob@example.com", cfgExtKnown, false, false, "", "", false},
		{"automated sender suppressed despite internal", mail.Header{"Auto-Submitted": {"auto-replied"}}, "bob@example.com", cfgExtAll, true, false, "", "", false},
		{"self suppressed", mail.Header{}, selfAddr, cfg, true, false, "", "", false},
	}
	for _, c := range cases {
		subject, body, send := autoReplyDecision(c.hdr, c.sender, selfAddr, c.cfg, c.internal, c.senderKnown)
		if send != c.wantSend || body != c.wantBody || subject != c.wantSubject {
			t.Errorf("%s: decision = (%q,%q,%v), want (%q,%q,%v)", c.name, subject, body, send, c.wantSubject, c.wantBody, c.wantSend)
		}
	}
}

// TestAutoSubmittedKeyword checks the leading keyword is isolated from
// parameters and comments so the "not no" comparison is exact.
func TestAutoSubmittedKeyword(t *testing.T) {
	cases := map[string]string{
		"no":                        "no",
		"no; x":                     "no",
		"auto-replied":              "auto-replied",
		"auto-generated (rejected)": "auto-generated",
		"auto-notified; owner":      "auto-notified",
	}
	for in, want := range cases {
		if got := autoSubmittedKeyword(in); got != want {
			t.Errorf("autoSubmittedKeyword(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuildAutoReply checks the synthesized reply is a parseable RFC 5322
// message carrying the loop-break header, the right envelope, and a body that
// round-trips through quoted-printable.
func TestBuildAutoReply(t *testing.T) {
	when := time.Unix(1700000000, 0)
	raw, err := buildAutoReply("me@hermex.test", "bob@example.com", "Away until Monday", "I am out of office.", "<orig@example.com>", when)
	if err != nil {
		t.Fatalf("auto-reply does not build: %v", err)
	}

	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("reply is not a parseable message: %v", err)
	}
	wantEq(t, "Auto-Submitted", msg.Header.Get("Auto-Submitted"), "auto-replied")
	wantEq(t, "From", msg.Header.Get("From"), "me@hermex.test")
	wantEq(t, "To", msg.Header.Get("To"), "bob@example.com")
	wantEq(t, "In-Reply-To", msg.Header.Get("In-Reply-To"), "<orig@example.com>")
	wantEq(t, "References", msg.Header.Get("References"), "<orig@example.com>")
	wantEq(t, "Subject", msg.Header.Get("Subject"), "Away until Monday")
	body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatalf("decode the quoted-printable body: %v", err)
	}
	wantEq(t, "body", string(body), "I am out of office.")
}

// TestBuildAutoReplyDefaultsAndEncoding checks an empty subject falls back to a
// default and a non-ASCII subject is RFC 2047 encoded (and decodes back).
func TestBuildAutoReplyDefaultsAndEncoding(t *testing.T) {
	when := time.Unix(1700000000, 0)
	raw, err := buildAutoReply("me@hermex.test", "bob@example.com", "", "body", "", when)
	if err != nil {
		t.Fatalf("auto-reply does not build: %v", err)
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.Header.Get("Subject"); got != "Automatic reply" {
		t.Errorf("empty subject did not default: %q", got)
	}
	if got := msg.Header.Get("References"); got != "" {
		t.Errorf("empty inReplyTo emitted References %q", got)
	}

	raw, err = buildAutoReply("me@hermex.test", "bob@example.com", "Ofis dışındayım", "body", "", when)
	if err != nil {
		t.Fatalf("auto-reply does not build: %v", err)
	}
	msg, err = mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mime.WordDecoder)
	got, err := dec.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Ofis dışındayım" {
		t.Errorf("encoded subject did not decode back: %q", got)
	}
}
