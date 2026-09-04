package webmail2api

import (
	"bytes"
	"slices"
	"testing"
)

// TestMeetingAttendeeRejectsHeaderInjection is the attendee injection defect. An
// attendee value that failed to parse was kept as the raw string, and that string
// reaches both the invite's To header and the iCalendar ATTENDEE line. A line
// break in it therefore spliced headers of the organizer's choosing into a message
// the server relays externally, or ended the header block and pushed the rest into
// the body. The summary on the same builder was already flattened; the address was
// not.
func TestMeetingAttendeeRejectsHeaderInjection(t *testing.T) {
	raw, recipients, err := buildMeetingRequest("organizer@hermex.test", eventJSON{
		Summary:   "Sync",
		Attendees: []string{"victim@ext.example", "a@b.example\r\nBcc: attacker@evil.example"},
		Start:     "2026-09-01T10:00:00Z",
		End:       "2026-09-01T11:00:00Z",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	header, _, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
	for line := range bytes.SplitSeq(header, []byte("\r\n")) {
		if bytes.HasPrefix(bytes.ToLower(line), []byte("bcc:")) {
			t.Errorf("an attendee line break injected a header line: %q", line)
		}
	}
	if bytes.Contains(raw, []byte("attacker@evil.example")) {
		t.Errorf("the unparseable attendee was carried into the message:\n%s", raw)
	}
	if slices.Contains(recipients, "a@b.example\r\nbcc: attacker@evil.example") {
		t.Errorf("the unparseable attendee became a recipient: %v", recipients)
	}
	// The control in the same call: a valid attendee must still be invited.
	if !slices.Contains(recipients, "victim@ext.example") {
		t.Errorf("the valid attendee was lost: %v", recipients)
	}
}
