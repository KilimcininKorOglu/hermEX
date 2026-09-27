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
	head, body, _ := strings.Cut(string(raw), "\r\n\r\n")
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
	head := string(raw[:bytes.Index(raw, []byte("\r\n\r\n"))])
	if !strings.Contains(head, "From: <erin@hermex.test>") || !strings.Contains(head, "Sender: <alice@hermex.test>") {
		t.Errorf("headers do not name both identities:\n%s", head)
	}
}
