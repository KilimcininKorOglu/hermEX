package oxcmail

import (
	"strconv"
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
	mustNoErr(t, err, "import")

	wantEq(t, getString(t, msg.Props, mapi.PrMessageClass, "message class"), "IPM.Note", "message class")
	checkSubjectSplit(t, msg)
	checkOriginatorIdentity(t, msg)
	checkRecipientTable(t, msg)
	checkEnvelopeIdentity(t, msg)
	checkEnvelopeTimes(t, msg)

	// Importance from the header; sensitivity defaulted.
	wantEq(t, getInt32(t, msg.Props, mapi.PrImportance, "importance"), int32(mapi.ImportanceHigh), "importance")
	wantEq(t, getInt32(t, msg.Props, mapi.PrSensitivity, "sensitivity"), int32(mapi.SensitivityNone), "sensitivity")

	// Transport headers captured verbatim.
	th := getString(t, msg.Props, mapi.PrTransportMessageHeaders, "transport headers")
	wantContains(t, th, "Message-ID: <msg123@example.com>", "the transport headers keep the original Message-ID line")

	// Body decoded to text.
	wantEq(t, getString(t, msg.Props, mapi.PrBody, "body"), "Hello,\r\nThis is the body.\r\n", "body")
}

// checkSubjectSplit asserts the subject and its prefix/normalized split.
func checkSubjectSplit(t *testing.T, msg *Message) {
	t.Helper()
	wantEq(t, getString(t, msg.Props, mapi.PrSubject, "subject"), "Re: Project status", "subject")
	wantEq(t, getString(t, msg.Props, mapi.PrSubjectPrefix, "subject prefix"), "Re: ", "subject prefix")
	wantEq(t, getString(t, msg.Props, mapi.PrNormalizedSubject, "normalized subject"), "Project status", "normalized subject")
}

// checkOriginatorIdentity asserts that From populates the sent-representing
// identity and that, with no Sender header, the sender identity is filled from it.
func checkOriginatorIdentity(t *testing.T, msg *Message) {
	t.Helper()
	wantEq(t, getString(t, msg.Props, mapi.PrSentRepresentingName, "representing name"), "Alice Example", "representing name")
	wantEq(t, getString(t, msg.Props, mapi.PrSentRepresentingSmtpAddress, "representing smtp"), "alice@example.com", "representing smtp")
	wantEq(t, getString(t, msg.Props, mapi.PrSentRepresentingAddrType, "representing addrtype"), "SMTP", "representing addrtype")
	wantEq(t, getString(t, msg.Props, mapi.PrSenderName, "sender name"), "Alice Example", "sender name, filled from representing")
	wantEq(t, getString(t, msg.Props, mapi.PrSenderSmtpAddress, "sender smtp"), "alice@example.com", "sender smtp, filled from representing")

	// Sender search key: "SMTP:" + uppercased address + trailing NUL.
	v, ok := msg.Props.Get(mapi.PrSenderSearchKey)
	if !ok {
		t.Fatal("sender search key missing")
	}
	key, _ := v.([]byte)
	wantEq(t, string(key), "SMTP:ALICE@EXAMPLE.COM\x00", "sender search key")
}

// checkRecipientTable asserts the recipient bags: two To, one Cc, in header order.
func checkRecipientTable(t *testing.T, msg *Message) {
	t.Helper()
	if len(msg.Recipients) != 3 {
		t.Fatalf("recipients = %d, want 3", len(msg.Recipients))
	}
	for i, w := range []struct {
		name, smtp string
		typ        int32
	}{
		{"Bob", "bob@example.org", mapi.RecipTo},
		{"carol@example.net", "carol@example.net", mapi.RecipTo},
		{"Dave", "dave@example.com", mapi.RecipCc},
	} {
		r := msg.Recipients[i]
		label := "recipient " + strconv.Itoa(i)
		wantEq(t, getString(t, r, mapi.PrDisplayName, "rcpt display name"), w.name, label+" name")
		wantEq(t, getString(t, r, mapi.PrSmtpAddress, "rcpt smtp"), w.smtp, label+" smtp")
		wantEq(t, getInt32(t, r, mapi.PrRecipientType, "rcpt type"), w.typ, label+" type")
		wantEq(t, getInt32(t, r, mapi.PrObjectType, "rcpt object type"), int32(mapi.ObjectTypeMailUser), label+" object type")
	}
}

// checkEnvelopeIdentity asserts the ids and references, which are set verbatim.
func checkEnvelopeIdentity(t *testing.T, msg *Message) {
	t.Helper()
	wantEq(t, getString(t, msg.Props, mapi.PrInternetMessageID, "message id"), "<msg123@example.com>", "message id")
	wantEq(t, getString(t, msg.Props, mapi.PrInternetReferences, "references"),
		"<prev1@example.com> <prev2@example.com>", "references")
	wantEq(t, getString(t, msg.Props, mapi.PrInReplyToID, "in-reply-to"), "<prev2@example.com>", "in-reply-to")
}

// checkEnvelopeTimes asserts the submit time is the parsed Date converted to NT
// time, and that the creation time mirrors it.
func checkEnvelopeTimes(t *testing.T, msg *Message) {
	t.Helper()
	want := mapi.UnixToNTTime(time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC))
	submit, ok := msg.Props.Get(mapi.PrClientSubmitTime)
	if !ok {
		t.Fatal("submit time missing")
	}
	got, _ := submit.(uint64)
	wantEq(t, got, want, "submit time")

	created, ok := msg.Props.Get(mapi.PrCreationTime)
	if !ok {
		t.Fatal("creation time missing")
	}
	gotCreated, _ := created.(uint64)
	wantEq(t, gotCreated, want, "creation time")
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
