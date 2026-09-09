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
// part's Content-Type, and that without one the message stays a plain leaf.
func TestExportCalendarAlternative(t *testing.T) {
	msg := &Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Accepted: Quarterly Review"},
		{Tag: mapi.PrBody, Value: "Alice has accepted."},
		{Tag: mapi.PrSenderSmtpAddress, Value: "alice@hermex.test"},
	}}
	ical := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REPLY\r\n" +
		"BEGIN:VEVENT\r\nUID:meeting-42\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")

	wire, err := Export(msg, Options{CalendarBody: ical, CalendarMethod: "REPLY"})
	mustNoErr(t, err, "export with a calendar body")
	m, err := mail.ReadMessage(bytes.NewReader(wire))
	if err != nil {
		t.Fatalf("exported message not parseable: %v\n%s", err, wire)
	}
	mediaType, params, err := stdmime.ParseMediaType(m.Header.Get("Content-Type"))
	mustNoErr(t, err, "parse the top Content-Type")
	wantEq(t, mediaType, "multipart/alternative", "top media type")

	alternatives := readAlternatives(t, m.Body, params["boundary"])
	calendar, ok := alternatives["text/calendar"]
	if !ok {
		t.Fatal("no text/calendar alternative in the exported message")
	}
	wantEq(t, calendar.params["method"], "REPLY", "calendar part method")
	wantContains(t, calendar.body, "UID:meeting-42", "the calendar part carries the iCalendar body")

	plainPart, ok := alternatives["text/plain"]
	if !ok {
		t.Fatal("no text/plain alternative in the exported message")
	}
	wantContains(t, plainPart.body, "Alice has accepted.", "the text alternative carries the body")

	// Without a calendar body the message stays a plain leaf, no calendar part.
	plain, err := Export(msg, Options{})
	mustNoErr(t, err, "export without a calendar body")
	wantNotContains(t, string(plain), "text/calendar", "an export without CalendarBody emits no calendar part")
}

// alternativePart is one branch of a multipart/alternative: its Content-Type
// parameters and its decoded body.
type alternativePart struct {
	params map[string]string
	body   string
}

// readAlternatives reads a multipart body into its parts, keyed by media type.
func readAlternatives(t *testing.T, body io.Reader, boundary string) map[string]alternativePart {
	t.Helper()
	out := map[string]alternativePart{}
	mr := multipart.NewReader(body, boundary)
	for {
		p, err := mr.NextPart()
		if err != nil {
			return out
		}
		mt, pp, _ := stdmime.ParseMediaType(p.Header.Get("Content-Type"))
		raw, _ := io.ReadAll(p)
		out[mt] = alternativePart{params: pp, body: string(raw)}
	}
}
