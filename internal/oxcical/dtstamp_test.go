package oxcical

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestRequestDTSTAMPRoundTrips proves a request keeps the DTSTAMP the organizer
// gave it ([MS-OXCICAL] DTSTAMP: imported as PidLidOwnerCriticalChange and
// exported from it), rather than being re-stamped with the meeting's start.
func TestRequestDTSTAMPRoundTrips(t *testing.T) {
	r := newResolver()
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:s-1\r\n" +
		"DTSTAMP:20260601T080000Z\r\nDTSTART:20260612T090000Z\r\nDTEND:20260612T100000Z\r\n" +
		"SUMMARY:Review\r\nORGANIZER:mailto:alice@example.test\r\nATTENDEE:mailto:bob@example.test\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	msg, err := Import([]byte(body), r.opt())
	mustNoErr(t, err, "import")
	raw, err := Export(msg, r.opt())
	mustNoErr(t, err, "export")
	if !strings.Contains(string(raw), "DTSTAMP:20260601T080000Z\r\n") {
		t.Errorf("the request's DTSTAMP did not survive:\n%s", raw)
	}
}

// TestCounterDTSTAMPIsTheAttendees proves a COUNTER's DTSTAMP is stored as the
// attendee's (PidLidAttendeeCriticalChange) and exported from it.
func TestCounterDTSTAMPIsTheAttendees(t *testing.T) {
	r := newResolver()
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:COUNTER\r\nBEGIN:VEVENT\r\nUID:s-2\r\n" +
		"DTSTAMP:20260602T080000Z\r\nDTSTART:20260612T110000Z\r\nDTEND:20260612T113000Z\r\n" +
		"ORGANIZER:mailto:alice@example.test\r\nATTENDEE;PARTSTAT=TENTATIVE:mailto:bob@example.test\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	msg, err := Import([]byte(body), r.opt())
	mustNoErr(t, err, "import")
	if _, ok := msg.Props.Get(r.tag(mapi.NameAttendeeCriticalChange, mapi.PtSysTime)); !ok {
		t.Error("a COUNTER's DTSTAMP is not stored as PidLidAttendeeCriticalChange")
	}
	raw, err := Export(msg, r.opt())
	mustNoErr(t, err, "export")
	if !strings.Contains(string(raw), "DTSTAMP:20260602T080000Z\r\n") {
		t.Errorf("the counter's DTSTAMP did not survive:\n%s", raw)
	}
}
