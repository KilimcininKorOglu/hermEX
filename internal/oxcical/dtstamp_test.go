package oxcical

import (
	"strings"
	"testing"
	"time"

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
	stamp, ok := r.timeVal(msg, mapi.NameAttendeeCriticalChange)
	if !ok {
		t.Error("a COUNTER's DTSTAMP is not stored as PidLidAttendeeCriticalChange")
	} else if want := mapi.UnixToNTTime(time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)); stamp != want {
		t.Errorf("PidLidAttendeeCriticalChange = %d, want the DTSTAMP 2026-06-02T08:00Z (%d)", stamp, want)
	}
	raw, err := Export(msg, r.opt())
	mustNoErr(t, err, "export")
	if !strings.Contains(string(raw), "DTSTAMP:20260602T080000Z\r\n") {
		t.Errorf("the counter's DTSTAMP did not survive:\n%s", raw)
	}
}

// plainEvent is a stored calendar object with no METHOD and no DTSTAMP, so
// import records no critical-change time for it.
const plainEvent = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:s-3\r\n" +
	"DTSTART:20260612T090000Z\r\nDTEND:20260612T100000Z\r\nSUMMARY:Plain\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// TestStoredEventDTSTAMPIsLastModification proves a stored object without a
// critical-change time exports its PidTagLastModificationTime as DTSTAMP (RFC 5545
// 3.8.7.2: without METHOD, DTSTAMP is when the object was last revised in the
// store), so repeated reads of an unchanged object agree with its strong ETag.
func TestStoredEventDTSTAMPIsLastModification(t *testing.T) {
	r := newResolver()
	msg, err := Import([]byte(plainEvent), r.opt())
	mustNoErr(t, err, "import")
	revised := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	msg.Props = append(msg.Props, mapi.TaggedPropVal{Tag: mapi.PrLastModificationTime, Value: mapi.UnixToNTTime(revised)})
	raw, err := Export(msg, r.opt())
	mustNoErr(t, err, "export")
	if !strings.Contains(string(raw), "DTSTAMP:20260304T050607Z\r\n") {
		t.Errorf("DTSTAMP is not the last modification time 2026-03-04T05:06:07Z:\n%s", raw)
	}
}

// TestStoredEventDTSTAMPIgnoresMistypedModification proves a
// PidTagLastModificationTime that is not a PtSysTime is not read as a time.
func TestStoredEventDTSTAMPIgnoresMistypedModification(t *testing.T) {
	r := newResolver()
	msg, err := Import([]byte(plainEvent), r.opt())
	mustNoErr(t, err, "import")
	msg.Props = append(msg.Props, mapi.TaggedPropVal{Tag: mapi.PrLastModificationTime, Value: int32(0)})
	before := time.Now().UTC().Truncate(time.Second)
	raw, err := Export(msg, r.opt())
	mustNoErr(t, err, "export")
	after := time.Now().UTC()
	_, rest, found := strings.Cut(string(raw), "DTSTAMP:")
	if !found || len(rest) < 16 {
		t.Fatalf("no DTSTAMP in the export:\n%s", raw)
	}
	stamp, err := time.Parse("20060102T150405Z", rest[:16])
	mustNoErr(t, err, "parse DTSTAMP")
	if stamp.Before(before) || stamp.After(after) {
		t.Errorf("DTSTAMP = %s, want the export time between %s and %s", stamp, before, after)
	}
}
