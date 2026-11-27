package oxcmail

import (
	"bytes"
	"errors"
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

// TestExportRefusesACalendarMethodThatIsNoToken passes a method carrying a line
// break. RFC 5322 2.2 ends a field at CRLF, so a break inside the Content-Type
// parameter would start a header field of the caller's choosing, and RFC 5545
// 3.7.2 makes METHOD an iana-token, which holds no such character: the export
// must refuse it rather than emit any bytes.
func TestExportRefusesACalendarMethodThatIsNoToken(t *testing.T) {
	msg := &Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Accepted"},
		{Tag: mapi.PrBody, Value: "ok"},
	}}
	ical := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REPLY\r\nEND:VCALENDAR\r\n")
	for _, method := range []string{"REPLY\r\nBcc: evil@attacker.example", "REPLY\nX: y", "RE PLY", "REPLY;x=y"} {
		wire, err := Export(msg, Options{CalendarBody: ical, CalendarMethod: method})
		if err == nil {
			t.Errorf("method %q was accepted:\n%s", method, wire)
		}
		if wire != nil {
			t.Errorf("method %q was refused but %d bytes came back", method, len(wire))
		}
	}
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
		if errors.Is(err, io.EOF) {
			return out
		}
		mustNoErr(t, err, "read the next part")
		mt, pp, err := stdmime.ParseMediaType(p.Header.Get("Content-Type"))
		mustNoErr(t, err, "parse the part Content-Type")
		raw, err := io.ReadAll(p)
		mustNoErr(t, err, "read the part body")
		out[mt] = alternativePart{params: pp, body: string(raw)}
	}
}
