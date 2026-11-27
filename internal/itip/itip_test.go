package itip

import (
	"bytes"
	"strings"
	"testing"
)

// TestMessageCarriesTheSchedulingPart pins what a receiving client needs to process
// the message: a From naming the originator, a Date and a Message-ID (a mail
// without them is refused or misfiled by strict servers), the recipients in plain
// form, and a text/calendar part whose method matches the one inside the object.
func TestMessageCarriesTheSchedulingPart(t *testing.T) {
	raw, err := Message(Mail{
		From:     "alice@hermex.test",
		To:       []string{"mailto:bob@hermex.test", "carol@hermex.test"},
		Subject:  "Canceled: Sync",
		Text:     "The meeting is canceled.",
		Calendar: []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:CANCEL\r\nEND:VCALENDAR\r\n"),
		Method:   "CANCEL",
	})
	if err != nil {
		t.Fatal(err)
	}
	head, body, found := strings.Cut(string(raw), "\r\n\r\n")
	if !found {
		t.Fatalf("the message has no blank line between header and body:\n%s", raw)
	}
	for _, want := range []string{"From: <alice@hermex.test>", "Date: ", "Message-ID: <", "bob@hermex.test", "carol@hermex.test", "Subject: Canceled: Sync"} {
		if !strings.Contains(head, want) {
			t.Errorf("header lacks %q:\n%s", want, head)
		}
	}
	if strings.Contains(head, "mailto:") {
		t.Errorf("a recipient kept its calendar scheme:\n%s", head)
	}
	if !strings.Contains(body, "method=CANCEL") || !strings.Contains(body, "The meeting is canceled.") {
		t.Errorf("body lacks the calendar method or the text alternative:\n%s", body)
	}
}

// TestHostileSubjectAndMethodAddNoHeader feeds a subject and a method carrying a
// CRLF and a forged header. The subject comes from a meeting's SUMMARY, which an
// invitation from outside sets. RFC 5322 2.2 ends a header field at CRLF, so an
// unencoded line break would let the sender add a Bcc to the outgoing mail; the
// rendered head must hold no such field and the method must not leave its
// Content-Type parameter.
func TestHostileSubjectAndMethodAddNoHeader(t *testing.T) {
	raw, err := Message(Mail{
		From:     "alice@hermex.test",
		To:       []string{"bob@hermex.test"},
		Subject:  "Sync\r\nBcc: eve@evil.test",
		Calendar: []byte("BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nEND:VCALENDAR\r\n"),
		Method:   "REQUEST\r\nX-Injected: 1",
	})
	if err != nil {
		return // refusing the message is also safe
	}
	for line := range strings.SplitSeq(string(raw), "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") || strings.HasPrefix(line, "X-Injected:") {
			t.Errorf("a hostile value added its own line: %q", line)
		}
	}
}

// TestOnBehalfMessageNamesBothIdentities proves a delegate's scheduling message
// carries the originator in From and the delegate in Sender.
func TestOnBehalfMessageNamesBothIdentities(t *testing.T) {
	raw, err := Message(Mail{
		From: "erin@hermex.test", Sender: "alice@hermex.test", To: []string{"bob@hermex.test"},
		Subject: "Proposed", Calendar: []byte("BEGIN:VCALENDAR\r\nMETHOD:COUNTER\r\nEND:VCALENDAR\r\n"), Method: "COUNTER",
	})
	if err != nil {
		t.Fatal(err)
	}
	head, _, found := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !found {
		t.Fatalf("the message has no blank line between header and body:\n%s", raw)
	}
	if !bytes.Contains(head, []byte("From: <erin@hermex.test>")) || !bytes.Contains(head, []byte("Sender: <alice@hermex.test>")) {
		t.Errorf("headers do not name both identities:\n%s", head)
	}
}
