package mime

import (
	"bytes"
	"testing"
)

func TestParseEnvelope(t *testing.T) {
	raw := "From: \"Alice\" <alice@example.com>\r\n" +
		"To: bob@example.com, Carol <carol@example.org>\r\n" +
		"Subject: =?UTF-8?Q?caf=C3=A9?=\r\n" +
		"Date: Tue, 14 Nov 2023 22:13:20 +0000\r\n" +
		"Message-ID: <abc@example.com>\r\n" +
		"\r\nbody text"

	env, err := ParseEnvelope([]byte(raw))
	mustNoErr(t, err, "parse the envelope")

	// RFC 2047 encoded-word subject must be decoded, while RawSubject retains
	// the 7-bit wire text for IMAP ENVELOPE (RFC 3501).
	wantEq(t, env.Subject, "café", "subject")
	wantEq(t, env.RawSubject, "=?UTF-8?Q?caf=C3=A9?=", "raw subject")
	wantEq(t, env.RawDate, "Tue, 14 Nov 2023 22:13:20 +0000", "raw date")
	// 2023-11-14T22:13:20Z is exactly unix 1700000000.
	wantEq(t, env.Date.Unix(), int64(1700000000), "date")
	wantEq(t, env.MessageID, "<abc@example.com>", "message id")

	if len(env.From) != 1 {
		t.Fatalf("From = %#v, want one address", env.From)
	}
	wantAddr(t, env.From[0], "Alice", "alice", "example.com", "From")
	if len(env.To) != 2 {
		t.Fatalf("To = %#v, want two addresses", env.To)
	}
	wantAddr(t, env.To[1], "Carol", "carol", "example.org", "the second To")

	// Sender and Reply-To default to From when absent (RFC 3501).
	if len(env.Sender) != 1 || len(env.ReplyTo) != 1 {
		t.Fatalf("Sender = %#v, ReplyTo = %#v, want one each", env.Sender, env.ReplyTo)
	}
	wantEq(t, env.Sender[0].Mailbox, "alice", "the Sender default")
	wantEq(t, env.ReplyTo[0].Mailbox, "alice", "the Reply-To default")
	wantEq(t, len(env.Cc), 0, "Cc addresses")
	wantEq(t, len(env.Bcc), 0, "Bcc addresses")
}

// TestParseEnvelopeSplitsAtTheLastAt proves the ENVELOPE host is the domain after
// the last '@' (RFC 3501 host-name, RFC 5321 section 4.1.2). A quoted local part
// may hold an '@'; split at the first one, the client showed the sender in a
// domain that is part of its local part.
func TestParseEnvelopeSplitsAtTheLastAt(t *testing.T) {
	raw := "From: \"x@y\"@example.com\r\nSubject: s\r\n\r\nbody"
	env, err := ParseEnvelope([]byte(raw))
	mustNoErr(t, err, "parse the envelope")
	if len(env.From) != 1 {
		t.Fatalf("From = %#v, want one address", env.From)
	}
	wantAddr(t, env.From[0], "", "x@y", "example.com", "From")
}

func TestParseEnvelopeMalformed(t *testing.T) {
	garbage := []byte("this is not a message")
	orig := append([]byte(nil), garbage...)
	env, err := ParseEnvelope(garbage)
	if err == nil {
		t.Error("ParseEnvelope(garbage) = nil error, want error")
	}
	if env != nil {
		t.Errorf("ParseEnvelope(garbage) = %#v, want nil envelope", env)
	}
	if !bytes.Equal(garbage, orig) {
		t.Errorf("ParseEnvelope(garbage) mutated input: got %q, want %q", garbage, orig)
	}

	env, err = ParseEnvelope(nil)
	if err == nil {
		t.Error("ParseEnvelope(nil) = nil error, want error")
	}
	if env != nil {
		t.Errorf("ParseEnvelope(nil) = %#v, want nil envelope", env)
	}
}

// TestParseEnvelopeExplicitSenderAndReplyTo verifies that explicit Sender,
// Reply-To, Cc, Bcc, and In-Reply-To headers are preserved instead of being
// overwritten by From (RFC 3501 section 7.4.2).
func TestParseEnvelopeExplicitSenderAndReplyTo(t *testing.T) {
	raw := "From: Alice <alice@example.com>\r\n" +
		"Sender: List Bot <bot@example.com>\r\n" +
		"Reply-To: Support <support@example.org>\r\n" +
		"Cc: Dave <dave@example.net>\r\n" +
		"Bcc: Eve <eve@example.net>\r\n" +
		"In-Reply-To: <prev@example.com>\r\n" +
		"\r\nbody"

	env, err := ParseEnvelope([]byte(raw))
	mustNoErr(t, err, "parse envelope with explicit addressing headers")

	if len(env.Sender) != 1 || len(env.ReplyTo) != 1 || len(env.Cc) != 1 || len(env.Bcc) != 1 {
		t.Fatalf("unexpected address counts: Sender=%d ReplyTo=%d Cc=%d Bcc=%d",
			len(env.Sender), len(env.ReplyTo), len(env.Cc), len(env.Bcc))
	}
	wantAddr(t, env.Sender[0], "List Bot", "bot", "example.com", "Sender")
	wantAddr(t, env.ReplyTo[0], "Support", "support", "example.org", "Reply-To")
	wantAddr(t, env.Cc[0], "Dave", "dave", "example.net", "Cc")
	wantAddr(t, env.Bcc[0], "Eve", "eve", "example.net", "Bcc")
	wantEq(t, env.InReplyTo, "<prev@example.com>", "In-Reply-To")
}

// TestParseEnvelopeHostileHeaders verifies best-effort handling of malformed
// address lists, unparseable dates, and unknown RFC 2047 charsets without
// fabricating addresses or losing raw header text.
func TestParseEnvelopeHostileHeaders(t *testing.T) {
	raw := "From: not valid <<<<\r\n" +
		"To: also broken >>>\r\n" +
		"Date: not-a-real-date\r\n" +
		"Subject: =?X-NO-SUCH-CHARSET?Q?abc?=\r\n" +
		"\r\nbody"

	env, err := ParseEnvelope([]byte(raw))
	mustNoErr(t, err, "parse envelope with hostile header values")

	wantEq(t, len(env.From), 0, "malformed From yields no addresses")
	wantEq(t, len(env.Sender), 0, "Sender defaults to empty From")
	wantEq(t, len(env.ReplyTo), 0, "ReplyTo defaults to empty From")
	wantEq(t, len(env.To), 0, "malformed To yields no addresses")
	wantEq(t, env.Date.IsZero(), true, "malformed Date stays zero")
	wantEq(t, env.RawDate, "not-a-real-date", "RawDate preserved verbatim")
	wantEq(t, env.Subject, "=?X-NO-SUCH-CHARSET?Q?abc?=", "unparseable RFC 2047 Subject falls back")
	wantEq(t, env.RawSubject, "=?X-NO-SUCH-CHARSET?Q?abc?=", "RawSubject preserved verbatim")
}
