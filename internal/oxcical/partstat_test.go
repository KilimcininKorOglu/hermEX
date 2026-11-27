package oxcical

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestAttendeePartstatRoundTrips proves an organizer's event keeps each attendee's
// response: an ATTENDEE's PARTSTAT imports as the recipient's
// PidTagRecipientTrackStatus and exports from it ([MS-OXCICAL] 2.1.3.1.1.20.2 and
// 2.1.3.1.1.20.2.3), and a status with no PARTSTAT exports none.
func TestAttendeePartstatRoundTrips(t *testing.T) {
	r := newResolver()
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:ps-1\r\nDTSTAMP:20260601T080000Z\r\n" +
		"DTSTART:20260612T090000Z\r\nDTEND:20260612T100000Z\r\nSUMMARY:Review\r\n" +
		"ORGANIZER:mailto:alice@example.test\r\n" +
		"ATTENDEE;PARTSTAT=DECLINED:mailto:bob@example.test\r\n" +
		"ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:carol@example.test\r\n" +
		"ATTENDEE;PARTSTAT=X-ACCEPTED-BY-FORCE:mailto:dave@example.test\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	msg, err := Import([]byte(body), r.opt())
	mustNoErr(t, err, "import")
	if len(msg.Recipients) != 3 {
		t.Fatalf("recipients = %d, want 3", len(msg.Recipients))
	}
	// RFC 5545 3.2.12: an unrecognized PARTSTAT is read as NEEDS-ACTION, so a value
	// the sender made up is never recorded as an answer.
	if got, ok := msg.Recipients[2].Get(mapi.PrRecipientTrackStatus); !ok || got != int32(0) {
		t.Errorf("dave's made-up status = %v (ok=%v), want 0 (no response)", got, ok)
	}
	if got, ok := msg.Recipients[0].Get(mapi.PrRecipientTrackStatus); !ok || got != int32(4) {
		t.Errorf("bob's track status = %v (ok=%v), want 4 (declined)", got, ok)
	}
	if got, ok := msg.Recipients[1].Get(mapi.PrRecipientTrackStatus); !ok || got != int32(0) {
		t.Errorf("carol's track status = %v (ok=%v), want 0 (no response)", got, ok)
	}
	raw, err := Export(msg, r.opt())
	mustNoErr(t, err, "export")
	out := string(raw)
	if !strings.Contains(out, "ATTENDEE;PARTSTAT=DECLINED:mailto:bob@example.test\r\n") {
		t.Errorf("bob's response was not exported:\n%s", out)
	}
	if !strings.Contains(out, "ATTENDEE:mailto:carol@example.test\r\n") {
		t.Errorf("carol, who has not answered, carries a PARTSTAT:\n%s", out)
	}
}
