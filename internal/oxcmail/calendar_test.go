package oxcmail

import (
	"bytes"
	"io"
	stdmime "mime"
	"mime/multipart"
	"net/mail"
	"testing"

	"hermex/internal/mapi"
)

// TestExportCalendarAlternative confirms a pre-rendered iTIP body is carried as a
// text/calendar alternative beside the text body, with the METHOD surfaced on the
// part's Content-Type — and that without one the message stays a plain leaf.
func TestExportCalendarAlternative(t *testing.T) {
	msg := &Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Accepted: Quarterly Review"},
		{Tag: mapi.PrBody, Value: "Alice has accepted."},
		{Tag: mapi.PrSenderSmtpAddress, Value: "alice@hermex.test"},
	}}
	ical := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REPLY\r\n" +
		"BEGIN:VEVENT\r\nUID:meeting-42\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")

	wire, err := Export(msg, Options{CalendarBody: ical, CalendarMethod: "REPLY"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(wire))
	if err != nil {
		t.Fatalf("exported message not parseable: %v\n%s", err, wire)
	}
	mediaType, params, err := stdmime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("top Content-Type = %q (%v), want multipart/alternative", mediaType, err)
	}

	mr := multipart.NewReader(m.Body, params["boundary"])
	var sawCalendar, sawPlain bool
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		mt, pp, _ := stdmime.ParseMediaType(p.Header.Get("Content-Type"))
		body, _ := io.ReadAll(p)
		switch mt {
		case "text/calendar":
			sawCalendar = true
			if pp["method"] != "REPLY" {
				t.Errorf("calendar part method = %q, want REPLY", pp["method"])
			}
			if !bytes.Contains(body, []byte("UID:meeting-42")) {
				t.Errorf("calendar part missing the iCalendar body:\n%s", body)
			}
		case "text/plain":
			sawPlain = true
			if !bytes.Contains(body, []byte("Alice has accepted.")) {
				t.Errorf("text/plain alternative = %q, want the body", body)
			}
		}
	}
	if !sawCalendar {
		t.Error("no text/calendar alternative in the exported message")
	}
	if !sawPlain {
		t.Error("no text/plain alternative in the exported message")
	}

	// Without a calendar body the message stays a plain leaf — no calendar part.
	plain, err := Export(msg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plain, []byte("text/calendar")) {
		t.Error("Export without CalendarBody must not emit a calendar part")
	}
}
