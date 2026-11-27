package mta

import (
	"mime"
	"net/mail"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestAStoredSubjectIsUsedVerbatim keeps the mailbox's own wording exactly as it
// was configured. A user who typed a subject gets that subject, never a prefix
// bolted onto it.
func TestAStoredSubjectIsUsedVerbatim(t *testing.T) {
	SetAutoReplyPrefix("Automatic reply")
	t.Cleanup(func() { SetAutoReplyPrefix("") })
	if got := composeAutoReplySubject("On holiday", "Q3 budget"); got != "On holiday" {
		t.Errorf("subject = %q, want the stored one", got)
	}
}

// TestAnEmptySubjectNamesTheMessageItAnswers is the feature. EWS and ActiveSync
// carry no subject field, so a mailbox turned out of office from Outlook or a
// phone stores none, and every reply used to go out under one fixed string with
// no hint of which message it answered.
func TestAnEmptySubjectNamesTheMessageItAnswers(t *testing.T) {
	SetAutoReplyPrefix("Automatic reply")
	t.Cleanup(func() { SetAutoReplyPrefix("") })
	if got := composeAutoReplySubject("", "Q3 budget"); got != "Automatic reply: Q3 budget" {
		t.Errorf("subject = %q, want the prefix and the original subject", got)
	}
	// An incoming message with no subject leaves the prefix alone rather than
	// producing a trailing colon.
	if got := composeAutoReplySubject("", ""); got != "Automatic reply" {
		t.Errorf("subject = %q, want the bare prefix", got)
	}
	if got := composeAutoReplySubject("   ", "   "); got != "Automatic reply" {
		t.Errorf("whitespace subject = %q, want the bare prefix", got)
	}
}

// TestThePrefixIsTheOperatorsWording proves the setting reaches the composed
// subject, which is what makes it operator-tunable rather than a constant.
func TestThePrefixIsTheOperatorsWording(t *testing.T) {
	t.Cleanup(func() { SetAutoReplyPrefix("") })
	SetAutoReplyPrefix("Otomatik yanıt")
	if got := composeAutoReplySubject("", "Q3 budget"); got != "Otomatik yanıt: Q3 budget" {
		t.Errorf("subject = %q, want the operator's prefix", got)
	}
	// An unset prefix falls back to the wording every reply carried before the
	// setting existed, so a deployment that never opens the form sees no change.
	SetAutoReplyPrefix("")
	if got := composeAutoReplySubject("", ""); got != DefaultAutoReplyPrefix {
		t.Errorf("subject = %q, want %q", got, DefaultAutoReplyPrefix)
	}
}

// TestTheOriginalSubjectIsDecoded keeps a non-ASCII subject readable. The
// incoming header is encoded-word encoded, and repeating it raw would put
// "=?utf-8?" into the reply the sender reads.
func TestTheOriginalSubjectIsDecoded(t *testing.T) {
	SetAutoReplyPrefix("Automatic reply")
	t.Cleanup(func() { SetAutoReplyPrefix("") })
	got := composeAutoReplySubject("", "=?utf-8?q?Toplant=C4=B1_notlar=C4=B1?=")
	if got != "Automatic reply: Toplantı notları" {
		t.Errorf("subject = %q, want the decoded original", got)
	}
}

// TestTheOriginalSubjectCannotSplitTheHeader is the injection guard. The subject
// is attacker-controlled and it is repeated into a header this server writes by
// hand, so a CR or LF in it must never start a second field.
func TestTheOriginalSubjectCannotSplitTheHeader(t *testing.T) {
	SetAutoReplyPrefix("Automatic reply")
	t.Cleanup(func() { SetAutoReplyPrefix("") })

	for _, rawSubj := range []string{
		"hi\r\nBcc: victim@elsewhere.test",
		"hi\nBcc: victim@elsewhere.test",
		"hi\rBcc: victim@elsewhere.test",
		"hi\x00\x7fBcc: victim@elsewhere.test",
	} {
		subject := composeAutoReplySubject("", rawSubj)
		if strings.ContainsAny(subject, "\r\n\x00\x7f") {
			t.Fatalf("the composed subject carries control characters: %q", subject)
		}

		raw, err := buildAutoReply("a@acme.test", "b@acme.test", subject, "away", "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		if got := msg.Header.Get("Bcc"); got != "" {
			t.Errorf("the incoming subject injected a Bcc header: %q", got)
		}
		// And the text still arrives, minus the control characters.
		decoded, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(decoded, "Bcc: victim@elsewhere.test") {
			t.Errorf("subject = %q, want the injected text carried as text", decoded)
		}
	}
	malformed := composeAutoReplySubject("", "=?unknown-cs?b?!!!?=\r\nBcc: evil@test")
	if strings.ContainsAny(malformed, "\r\n") {
		t.Errorf("malformed encoded subject kept line breaks: %q", malformed)
	}
}

// TestAConfiguredSubjectCannotSplitTheHeader covers the two subjects that skip
// the incoming subject's control-character strip: the operator's prefix, saved in
// the admin panel, and the subject a mailbox stored over webmail or the admin API.
// Either one with a CR or LF must still reach the reply as one Subject field.
func TestAConfiguredSubjectCannotSplitTheHeader(t *testing.T) {
	t.Cleanup(func() { SetAutoReplyPrefix("") })
	SetAutoReplyPrefix("Away\r\nBcc: prefix@elsewhere.test")
	for name, subject := range map[string]string{
		"the operator's prefix":        composeAutoReplySubject("", "Q3 budget"),
		"the mailbox's stored subject": composeAutoReplySubject("Away\r\nBcc: stored@elsewhere.test", "Q3 budget"),
	} {
		raw, err := buildAutoReply("a@acme.test", "b@acme.test", subject, "away", "", time.Now())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := msg.Header.Get("Bcc"); got != "" {
			t.Errorf("%s injected a Bcc header: %q", name, got)
		}
	}
}

// TestTheOriginalSubjectIsBounded keeps an absurd incoming subject from becoming
// an absurd outgoing header. The value is attacker-controlled and unbounded on
// the wire.
func TestTheOriginalSubjectIsBounded(t *testing.T) {
	SetAutoReplyPrefix("Automatic reply")
	t.Cleanup(func() { SetAutoReplyPrefix("") })
	got := composeAutoReplySubject("", strings.Repeat("x", 5000))
	if len(got) > len("Automatic reply: ")+maxOriginalSubject {
		t.Errorf("the composed subject is %d bytes", len(got))
	}
	mb := composeAutoReplySubject("", strings.Repeat("ğ", 250))
	if len(mb) > len("Automatic reply: ")+maxOriginalSubject {
		t.Errorf("multibyte subject is %d bytes", len(mb))
	}
	if !utf8.ValidString(mb) {
		t.Errorf("truncated multibyte subject is invalid UTF-8: %q", mb)
	}
}
