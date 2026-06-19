package oxcmail

import (
	"bytes"
	stdmime "mime"
	"net/mail"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// plainVector is a distinct-byte plain-text message used by the round-trip
// tests.
var plainVector = []byte("From: Alice Example <alice@example.com>\r\n" +
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

// TestExportWellFormed checks that Export produces a message a standard,
// independent parser (net/mail) accepts, with the key headers decoding to the
// expected values.
func TestExportWellFormed(t *testing.T) {
	msg, err := Import(plainVector, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	wire, err := Export(msg, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	m, err := mail.ReadMessage(bytes.NewReader(wire))
	if err != nil {
		t.Fatalf("exported message not parseable by net/mail: %v\n%s", err, wire)
	}

	from, err := mail.ParseAddress(m.Header.Get("From"))
	if err != nil {
		t.Fatalf("From not parseable: %v", err)
	}
	if from.Address != "alice@example.com" {
		t.Errorf("From address = %q", from.Address)
	}
	if from.Name != "Alice Example" {
		t.Errorf("From name = %q", from.Name)
	}
	// A message that named only From must not gain a Sender header.
	if s := m.Header.Get("Sender"); s != "" {
		t.Errorf("unexpected Sender header %q", s)
	}

	to, err := m.Header.AddressList("To")
	if err != nil {
		t.Fatalf("To not parseable: %v", err)
	}
	if len(to) != 2 || to[0].Address != "bob@example.org" || to[1].Address != "carol@example.net" {
		t.Errorf("To = %+v", to)
	}
	cc, err := m.Header.AddressList("Cc")
	if err != nil || len(cc) != 1 || cc[0].Address != "dave@example.com" {
		t.Errorf("Cc = %+v (err %v)", cc, err)
	}

	subj, err := (&stdmime.WordDecoder{}).DecodeHeader(m.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("Subject decode: %v", err)
	}
	if subj != "Re: Project status" {
		t.Errorf("Subject = %q", subj)
	}

	if got := m.Header.Get("Message-ID"); got != "<msg123@example.com>" {
		t.Errorf("Message-ID = %q", got)
	}

	d, err := m.Header.Date()
	if err != nil {
		t.Fatalf("Date parse: %v", err)
	}
	if want := time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC); !d.Equal(want) {
		t.Errorf("Date = %v, want %v", d, want)
	}

	body := make([]byte, 1024)
	n, _ := m.Body.Read(body)
	if got := string(body[:n]); got != "Hello,\r\nThis is the body.\r\n" {
		t.Errorf("body = %q", got)
	}
}

// TestExportImportRoundTrip checks that Export(Import(raw)) re-imports to the
// same core property set: the convert path preserves meaning even though the
// bytes are regenerated.
// TestEnsureMessageID covers the originating-message Message-ID policy: a
// submission path calls EnsureMessageID before Export, which assigns a unique id
// at the sender's domain when absent and preserves an explicit one; Export itself
// emits only a present id (so re-exporting a stored id-less message yields the same
// bytes on every read).
func TestEnsureMessageID(t *testing.T) {
	newSender := func() mapi.PropertyValues {
		return mapi.PropertyValues{
			{Tag: mapi.PrSenderSmtpAddress, Value: "alice@example.com"},
			{Tag: mapi.PrSenderAddrType, Value: "SMTP"},
			{Tag: mapi.PrSenderEmailAddress, Value: "alice@example.com"},
			{Tag: mapi.PrSubject, Value: "hi"},
		}
	}
	exported := func(t *testing.T, props mapi.PropertyValues) string {
		t.Helper()
		wire, err := Export(&Message{Props: props}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := mail.ReadMessage(bytes.NewReader(wire))
		if err != nil {
			t.Fatalf("exported message not parseable: %v\n%s", err, wire)
		}
		return parsed.Header.Get("Message-ID")
	}

	// Assigned at the sender's domain when absent, and emitted by Export.
	p1 := newSender()
	EnsureMessageID(&p1)
	id := exported(t, p1)
	if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@example.com>") {
		t.Errorf("assigned Message-ID = %q, want <token@example.com>", id)
	}
	// Each assignment is unique (a transmitted message needs a distinct one).
	p2 := newSender()
	EnsureMessageID(&p2)
	if id2 := exported(t, p2); id2 == id {
		t.Errorf("two assignments produced the same id %q, want random", id)
	}
	// An explicit Message-ID is preserved, never overridden.
	p3 := append(newSender(), mapi.TaggedPropVal{Tag: mapi.PrInternetMessageID, Value: "<keep@host>"})
	EnsureMessageID(&p3)
	if got := exported(t, p3); got != "<keep@host>" {
		t.Errorf("explicit Message-ID = %q, want it preserved", got)
	}
	// Export alone (no EnsureMessageID) emits no Message-ID — the reference
	// behaviour, keeping a re-served stored message byte-stable.
	if got := exported(t, newSender()); got != "" {
		t.Errorf("Export emitted Message-ID %q without one set, want none", got)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	msg1, err := Import(plainVector, Options{})
	if err != nil {
		t.Fatalf("Import 1: %v", err)
	}
	wire, err := Export(msg1, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	msg2, err := Import(wire, Options{})
	if err != nil {
		t.Fatalf("Import 2: %v", err)
	}

	stringProps := []mapi.PropTag{
		mapi.PrSubject, mapi.PrSubjectPrefix, mapi.PrNormalizedSubject,
		mapi.PrSentRepresentingName, mapi.PrSentRepresentingSmtpAddress,
		mapi.PrSenderSmtpAddress, mapi.PrInternetMessageID,
		mapi.PrInternetReferences, mapi.PrInReplyToID, mapi.PrBody,
	}
	for _, tag := range stringProps {
		v1 := propString(msg1.Props, tag)
		v2 := propString(msg2.Props, tag)
		if v1 != v2 {
			t.Errorf("%s drifted: %q -> %q", tag, v1, v2)
		}
	}

	int32Props := []mapi.PropTag{mapi.PrImportance, mapi.PrSensitivity}
	for _, tag := range int32Props {
		v1, _ := propInt32(msg1.Props, tag)
		v2, _ := propInt32(msg2.Props, tag)
		if v1 != v2 {
			t.Errorf("%s drifted: %d -> %d", tag, v1, v2)
		}
	}

	if v1, _ := propUint64(msg1.Props, mapi.PrClientSubmitTime); true {
		v2, _ := propUint64(msg2.Props, mapi.PrClientSubmitTime)
		if v1 != v2 {
			t.Errorf("submit time drifted: %d -> %d", v1, v2)
		}
	}

	// Recipient set (smtp + type) must be preserved, in order.
	if len(msg1.Recipients) != len(msg2.Recipients) {
		t.Fatalf("recipient count drifted: %d -> %d", len(msg1.Recipients), len(msg2.Recipients))
	}
	for i := range msg1.Recipients {
		s1 := propString(msg1.Recipients[i], mapi.PrSmtpAddress)
		s2 := propString(msg2.Recipients[i], mapi.PrSmtpAddress)
		t1, _ := propInt32(msg1.Recipients[i], mapi.PrRecipientType)
		t2, _ := propInt32(msg2.Recipients[i], mapi.PrRecipientType)
		if s1 != s2 || t1 != t2 {
			t.Errorf("recipient %d drifted: (%q,%d) -> (%q,%d)", i, s1, t1, s2, t2)
		}
	}
}

// TestExportNonASCII checks that a non-ASCII subject and body survive the
// convert path: the encoded-word subject and the quoted-printable body decode
// back to the originals.
func TestExportNonASCII(t *testing.T) {
	raw := []byte("From: a@b.com\r\n" +
		"Subject: =?utf-8?q?Caf=C3=A9_update?=\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n" +
		"\r\n" +
		"M\xc3\xa9\xc3\xa9sti \xc3\xa7ay\r\n")

	msg1, err := Import(raw, Options{})
	if err != nil {
		t.Fatalf("Import 1: %v", err)
	}
	if got := propString(msg1.Props, mapi.PrSubject); got != "Café update" {
		t.Fatalf("subject after import = %q", got)
	}
	if got := propString(msg1.Props, mapi.PrBody); got != "Méésti çay\r\n" {
		t.Fatalf("body after import = %q", got)
	}

	wire, err := Export(msg1, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	// The exported header block must be ASCII (the subject is encoded).
	headEnd := bytes.Index(wire, []byte("\r\n\r\n"))
	for i := range headEnd {
		if wire[i] > 0x7E {
			t.Fatalf("non-ASCII byte 0x%02x in exported header block", wire[i])
		}
	}

	msg2, err := Import(wire, Options{})
	if err != nil {
		t.Fatalf("Import 2: %v", err)
	}
	if got := propString(msg2.Props, mapi.PrSubject); got != "Café update" {
		t.Errorf("subject round-trip = %q", got)
	}
	if got := propString(msg2.Props, mapi.PrBody); got != "Méésti çay\r\n" {
		t.Errorf("body round-trip = %q", got)
	}
}

// TestExportMessageRFC822Verbatim checks that a message/rfc822 attachment is
// exported with a 7bit/8bit transfer encoding (never base64, per RFC 2046
// §5.2.1), so the encapsulated message is emitted verbatim and remains readable.
func TestExportMessageRFC822Verbatim(t *testing.T) {
	inner := "From: orig@example.com\r\nTo: rcpt@example.com\r\nSubject: Inner Subject\r\n\r\nInner body line.\r\n"
	raw := []byte("From: fwd@example.com\r\nTo: dest@example.com\r\nSubject: Fwd\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b0\"\r\n" +
		"\r\n" +
		"--b0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nSee attached.\r\n" +
		"--b0\r\n" +
		"Content-Type: message/rfc822\r\n" +
		"Content-Disposition: attachment\r\n\r\n" +
		inner +
		"--b0--\r\n")
	msg, err := Import(raw, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	wire, err := Export(msg, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !bytes.Contains(wire, []byte("message/rfc822")) {
		t.Fatalf("no message/rfc822 part:\n%s", wire)
	}
	// The encapsulated message appears verbatim; it would be unreadable if the
	// part had been base64-encoded instead of emitted 7bit/8bit.
	for _, want := range []string{"Subject: Inner Subject", "Inner body line."} {
		if !bytes.Contains(wire, []byte(want)) {
			t.Errorf("encapsulated message missing %q:\n%s", want, wire)
		}
	}
}
