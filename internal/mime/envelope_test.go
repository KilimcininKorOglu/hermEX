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
	if err != nil {
		t.Fatal(err)
	}
	// RFC 2047 encoded-word subject must be decoded.
	if env.Subject != "café" {
		t.Errorf("Subject = %q, want café", env.Subject)
	}
	// 2023-11-14T22:13:20Z is exactly unix 1700000000.
	if env.Date.Unix() != 1700000000 {
		t.Errorf("Date.Unix = %d, want 1700000000", env.Date.Unix())
	}
	if env.MessageID != "<abc@example.com>" {
		t.Errorf("MessageID = %q", env.MessageID)
	}
	if len(env.From) != 1 || env.From[0].Name != "Alice" ||
		env.From[0].Mailbox != "alice" || env.From[0].Host != "example.com" {
		t.Errorf("From = %#v", env.From)
	}
	if len(env.To) != 2 || env.To[1].Name != "Carol" ||
		env.To[1].Mailbox != "carol" || env.To[1].Host != "example.org" {
		t.Errorf("To = %#v", env.To)
	}
	// Sender and Reply-To default to From when absent (RFC 3501).
	if len(env.Sender) != 1 || env.Sender[0].Mailbox != "alice" {
		t.Errorf("Sender default = %#v", env.Sender)
	}
	if len(env.ReplyTo) != 1 || env.ReplyTo[0].Mailbox != "alice" {
		t.Errorf("ReplyTo default = %#v", env.ReplyTo)
	}
	if len(env.Cc) != 0 || len(env.Bcc) != 0 {
		t.Errorf("Cc=%v Bcc=%v, want both empty", env.Cc, env.Bcc)
	}
}

func TestParseEnvelopeMalformed(t *testing.T) {
	if _, err := ParseEnvelope([]byte("this is not a message")); err == nil {
		t.Error("ParseEnvelope(garbage) = nil error, want error")
	}
}
