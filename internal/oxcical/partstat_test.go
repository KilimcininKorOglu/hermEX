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
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	msg, err := Import([]byte(body), r.opt())
	mustNoErr(t, err, "import")
	if len(msg.Recipients) != 2 {
		t.Fatalf("recipients = %d, want 2", len(msg.Recipients))
	}
	if got, _ := msg.Recipients[0].Get(mapi.PrRecipientTrackStatus); got != int32(4) {
		t.Errorf("bob's track status = %v, want 4 (declined)", got)
	}
	if got, _ := msg.Recipients[1].Get(mapi.PrRecipientTrackStatus); got != int32(0) {
		t.Errorf("carol's track status = %v, want 0 (no response)", got)
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
