package oxcmail

import (
	"bytes"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// getString returns a string-typed property or fails the test.
func getString(t *testing.T, props mapi.PropertyValues, tag mapi.PropTag, label string) string {
	t.Helper()
	v, ok := props.Get(tag)
	if !ok {
		t.Fatalf("%s (%s) missing", label, tag)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s (%s) is %T, want string", label, tag, v)
	}
	return s
}

// getInt32 returns an int32-typed property or fails the test.
func getInt32(t *testing.T, props mapi.PropertyValues, tag mapi.PropTag, label string) int32 {
	t.Helper()
	v, ok := props.Get(tag)
	if !ok {
		t.Fatalf("%s (%s) missing", label, tag)
	}
	n, ok := v.(int32)
	if !ok {
		t.Fatalf("%s (%s) is %T, want int32", label, tag, v)
	}
	return n
}

// TestImportPlainEnvelope checks the core property set Import derives from a
// plain-text message: message class, subject (with prefix split), the
// sent-representing identity (From maps to representing, not sender), the
// sender fallback fill, the recipient table, envelope ids/times, importance,
// the default sensitivity, the verbatim transport headers, and the body.
func TestImportPlainEnvelope(t *testing.T) {
	raw := []byte("From: Alice Example <alice@example.com>\r\n" +
		"To: Bob <bob@example.org>, carol@example.net\r\n" +
		"Cc: Dave <dave@example.com>\r\n" +
		"Subject: Re: Project status\r\n" +
		"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\n" +
		"Message-ID: <msg123@example.com>\r\n" +
		"References: <prev1@example.com> <prev2@example.com>\r\n" +
		"In-Reply-To: <prev2@example.com>\r\n" +
		"Importance: High\r\n" +
		"\r\n" +
		"Hello,\r\nThis is the body.\r\n")

	msg, err := Import(raw, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if got := getString(t, msg.Props, mapi.PrMessageClass, "message class"); got != "IPM.Note" {
		t.Errorf("message class = %q, want IPM.Note", got)
	}

	// Subject and its prefix/normalized split.
	if got := getString(t, msg.Props, mapi.PrSubject, "subject"); got != "Re: Project status" {
		t.Errorf("subject = %q", got)
	}
	if got := getString(t, msg.Props, mapi.PrSubjectPrefix, "subject prefix"); got != "Re: " {
		t.Errorf("subject prefix = %q, want %q", got, "Re: ")
	}
	if got := getString(t, msg.Props, mapi.PrNormalizedSubject, "normalized subject"); got != "Project status" {
		t.Errorf("normalized subject = %q", got)
	}

	// From populates the sent-representing identity.
	if got := getString(t, msg.Props, mapi.PrSentRepresentingName, "representing name"); got != "Alice Example" {
		t.Errorf("representing name = %q", got)
	}
	if got := getString(t, msg.Props, mapi.PrSentRepresentingSmtpAddress, "representing smtp"); got != "alice@example.com" {
		t.Errorf("representing smtp = %q", got)
	}
	if got := getString(t, msg.Props, mapi.PrSentRepresentingAddrType, "representing addrtype"); got != "SMTP" {
		t.Errorf("representing addrtype = %q", got)
	}

	// With no Sender header, the sender identity is filled from representing.
	if got := getString(t, msg.Props, mapi.PrSenderName, "sender name"); got != "Alice Example" {
		t.Errorf("sender name = %q (fallback fill)", got)
	}
	if got := getString(t, msg.Props, mapi.PrSenderSmtpAddress, "sender smtp"); got != "alice@example.com" {
		t.Errorf("sender smtp = %q (fallback fill)", got)
	}

	// Sender search key: "SMTP:" + uppercased address + trailing NUL.
	if v, ok := msg.Props.Get(mapi.PrSenderSearchKey); !ok {
		t.Error("sender search key missing")
	} else if got, _ := v.([]byte); !bytes.Equal(got, []byte("SMTP:ALICE@EXAMPLE.COM\x00")) {
		t.Errorf("sender search key = %q", got)
	}

	// Recipient table: two To, one Cc, in order.
	if len(msg.Recipients) != 3 {
		t.Fatalf("recipients = %d, want 3", len(msg.Recipients))
	}
	wantRcpt := []struct {
		name, smtp string
		typ        int32
	}{
		{"Bob", "bob@example.org", mapi.RecipTo},
		{"carol@example.net", "carol@example.net", mapi.RecipTo},
		{"Dave", "dave@example.com", mapi.RecipCc},
	}
	for i, w := range wantRcpt {
		r := msg.Recipients[i]
		if got := getString(t, r, mapi.PrDisplayName, "rcpt display name"); got != w.name {
			t.Errorf("recipient %d name = %q, want %q", i, got, w.name)
		}
		if got := getString(t, r, mapi.PrSmtpAddress, "rcpt smtp"); got != w.smtp {
			t.Errorf("recipient %d smtp = %q, want %q", i, got, w.smtp)
		}
		if got := getInt32(t, r, mapi.PrRecipientType, "rcpt type"); got != w.typ {
			t.Errorf("recipient %d type = %d, want %d", i, got, w.typ)
		}
		if got := getInt32(t, r, mapi.PrObjectType, "rcpt object type"); got != mapi.ObjectTypeMailUser {
			t.Errorf("recipient %d object type = %d", i, got)
		}
	}

	// Envelope ids and references, set verbatim.
	if got := getString(t, msg.Props, mapi.PrInternetMessageID, "message id"); got != "<msg123@example.com>" {
		t.Errorf("message id = %q", got)
	}
	if got := getString(t, msg.Props, mapi.PrInternetReferences, "references"); got != "<prev1@example.com> <prev2@example.com>" {
		t.Errorf("references = %q", got)
	}
	if got := getString(t, msg.Props, mapi.PrInReplyToID, "in-reply-to"); got != "<prev2@example.com>" {
		t.Errorf("in-reply-to = %q", got)
	}

	// Submit time is the parsed Date converted to NT time.
	wantTime := mapi.UnixToNTTime(time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC))
	if v, ok := msg.Props.Get(mapi.PrClientSubmitTime); !ok {
		t.Error("submit time missing")
	} else if got, _ := v.(uint64); got != wantTime {
		t.Errorf("submit time = %d, want %d", got, wantTime)
	}
	// Creation time mirrors the submit time.
	if v, ok := msg.Props.Get(mapi.PrCreationTime); !ok || v.(uint64) != wantTime {
		t.Errorf("creation time = %v, want %d", v, wantTime)
	}

	// Importance from the header; sensitivity defaulted.
	if got := getInt32(t, msg.Props, mapi.PrImportance, "importance"); got != mapi.ImportanceHigh {
		t.Errorf("importance = %d, want High", got)
	}
	if got := getInt32(t, msg.Props, mapi.PrSensitivity, "sensitivity"); got != mapi.SensitivityNone {
		t.Errorf("sensitivity = %d, want None (default)", got)
	}

	// Transport headers captured verbatim.
	th := getString(t, msg.Props, mapi.PrTransportMessageHeaders, "transport headers")
	if !bytes.Contains([]byte(th), []byte("Message-ID: <msg123@example.com>")) {
		t.Errorf("transport headers missing original Message-ID line:\n%s", th)
	}

	// Body decoded to text.
	if got := getString(t, msg.Props, mapi.PrBody, "body"); got != "Hello,\r\nThis is the body.\r\n" {
		t.Errorf("body = %q", got)
	}
}

// TestImportSenderAndRepresenting checks that an explicit Sender header
// populates the sender identity and the representing identity is filled from it.
func TestImportSenderAndRepresenting(t *testing.T) {
	raw := []byte("From: Alice <alice@example.com>\r\n" +
		"Sender: Secretary <sec@example.com>\r\n" +
		"Subject: Hi\r\n" +
		"\r\n" +
		"body\r\n")
	msg, err := Import(raw, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := getString(t, msg.Props, mapi.PrSenderSmtpAddress, "sender smtp"); got != "sec@example.com" {
		t.Errorf("sender smtp = %q, want sec@example.com", got)
	}
	if got := getString(t, msg.Props, mapi.PrSentRepresentingSmtpAddress, "representing smtp"); got != "alice@example.com" {
		t.Errorf("representing smtp = %q, want alice@example.com", got)
	}
	// No prefix in "Hi": prefix empty, normalized equals subject.
	if got := getString(t, msg.Props, mapi.PrSubjectPrefix, "subject prefix"); got != "" {
		t.Errorf("subject prefix = %q, want empty", got)
	}
	if got := getString(t, msg.Props, mapi.PrNormalizedSubject, "normalized subject"); got != "Hi" {
		t.Errorf("normalized subject = %q, want Hi", got)
	}
}
