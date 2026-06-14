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
		{"auto-submitted auto-replied suppressed", mail.Header{"Auto-Submitted": {"auto-replied"}}, "bob@example.com", true},
		{"auto-submitted auto-generated suppressed", mail.Header{"Auto-Submitted": {"auto-generated"}}, "bob@example.com", true},
		{"auto-submitted keyword with comment suppressed", mail.Header{"Auto-Submitted": {"auto-generated (rejected)"}}, "bob@example.com", true},
		{"precedence absent replies", mail.Header{}, "bob@example.com", false},
		{"precedence bulk suppressed", mail.Header{"Precedence": {"bulk"}}, "bob@example.com", true},
		{"precedence list suppressed", mail.Header{"Precedence": {"list"}}, "bob@example.com", true},
		{"precedence junk suppressed", mail.Header{"Precedence": {"junk"}}, "bob@example.com", true},
		{"precedence other replies", mail.Header{"Precedence": {"first-class"}}, "bob@example.com", false},
		{"list-id suppressed", mail.Header{"List-Id": {"<list.example.com>"}}, "bob@example.com", true},
		{"list-unsubscribe suppressed", mail.Header{"List-Unsubscribe": {"<mailto:x@y>"}}, "bob@example.com", true},
		{"null sender suppressed", mail.Header{}, "", true},
		{"angle null sender suppressed", mail.Header{}, "<>", true},
		{"self suppressed", mail.Header{}, selfAddr, true},
		{"self with display name suppressed", mail.Header{}, "Me <" + selfAddr + ">", true},
		{"postmaster suppressed", mail.Header{}, "postmaster@example.com", true},
		{"mailer-daemon suppressed", mail.Header{}, "MAILER-DAEMON@example.com", true},
		{"no-reply suppressed", mail.Header{}, "no-reply@example.com", true},
		{"noreply suppressed", mail.Header{}, "noreply@example.com", true},
	}
	for _, c := range cases {
		if got := autoReplySuppressed(c.hdr, c.sender, selfAddr); got != c.want {
			t.Errorf("%s: autoReplySuppressed = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestAutoReplyDecision covers body selection and the external-reply gate. The
// external-enabled row is the one the delivery-path tests cannot reach (an
// external sender has no local mailbox, so its reply is dropped), so it is
// pinned here.
func TestAutoReplyDecision(t *testing.T) {
	cfg := objectstore.OOFSettings{InternalReply: "internal text", ExternalReply: "external text"}
	cfgExt := cfg
	cfgExt.ExternalEnabled = true

	cases := []struct {
		name     string
		hdr      mail.Header
		sender   string
		cfg      objectstore.OOFSettings
		internal bool
		wantBody string
		wantSend bool
	}{
		{"internal sender gets internal reply", mail.Header{}, "bob@example.com", cfg, true, "internal text", true},
		{"external sender with external disabled is silent", mail.Header{}, "bob@example.com", cfg, false, "", false},
		{"external sender with external enabled gets external reply", mail.Header{}, "bob@example.com", cfgExt, false, "external text", true},
		{"automated sender suppressed despite internal", mail.Header{"Auto-Submitted": {"auto-replied"}}, "bob@example.com", cfgExt, true, "", false},
		{"self suppressed", mail.Header{}, selfAddr, cfg, true, "", false},
	}
	for _, c := range cases {
		body, send := autoReplyDecision(c.hdr, c.sender, selfAddr, c.cfg, c.internal)
		if send != c.wantSend || body != c.wantBody {
			t.Errorf("%s: decision = (%q,%v), want (%q,%v)", c.name, body, send, c.wantBody, c.wantSend)
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
	raw := buildAutoReply("me@hermex.test", "bob@example.com", "Away until Monday", "I am out of office.", "<orig@example.com>", when)

	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("reply is not a parseable message: %v", err)
	}
	if got := msg.Header.Get("Auto-Submitted"); got != "auto-replied" {
		t.Errorf("Auto-Submitted = %q, want auto-replied (the loop break)", got)
	}
	if got := msg.Header.Get("From"); got != "me@hermex.test" {
		t.Errorf("From = %q", got)
	}
	if got := msg.Header.Get("To"); got != "bob@example.com" {
		t.Errorf("To = %q", got)
	}
	if got := msg.Header.Get("In-Reply-To"); got != "<orig@example.com>" {
		t.Errorf("In-Reply-To = %q", got)
	}
	if got := msg.Header.Get("Subject"); got != "Away until Monday" {
		t.Errorf("Subject = %q", got)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if string(body) != "I am out of office." {
		t.Errorf("body = %q, want round-tripped reply text", body)
	}
}

// TestBuildAutoReplyDefaultsAndEncoding checks an empty subject falls back to a
// default and a non-ASCII subject is RFC 2047 encoded (and decodes back).
func TestBuildAutoReplyDefaultsAndEncoding(t *testing.T) {
	when := time.Unix(1700000000, 0)
	raw := buildAutoReply("me@hermex.test", "bob@example.com", "", "body", "", when)
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.Header.Get("Subject"); got != "Automatic reply" {
		t.Errorf("empty subject did not default: %q", got)
	}

	raw = buildAutoReply("me@hermex.test", "bob@example.com", "Ofis dışındayım", "body", "", when)
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
