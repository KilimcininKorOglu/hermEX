package mime

import "testing"

func TestParseEnvelope(t *testing.T) {
	raw := "From: \"Alice\" <alice@example.com>\r\n" +
		"To: bob@example.com, Carol <carol@example.org>\r\n" +
		"Subject: =?UTF-8?Q?caf=C3=A9?=\r\n" +
		"Date: Tue, 14 Nov 2023 22:13:20 +0000\r\n" +
		"Message-ID: <abc@example.com>\r\n" +
		"\r\nbody text"

	env, err := ParseEnvelope([]byte(raw))
	mustNoErr(t, err, "parse the envelope")

	// RFC 2047 encoded-word subject must be decoded.
	wantEq(t, env.Subject, "café", "subject")
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

func TestParseEnvelopeMalformed(t *testing.T) {
	if _, err := ParseEnvelope([]byte("this is not a message")); err == nil {
		t.Error("ParseEnvelope(garbage) = nil error, want error")
	}
}
