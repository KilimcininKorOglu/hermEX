package oxcical

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/recurrence"
)

// seriesWithChanges is a daily Istanbul series at 10:00 with three occurrences
// removed and one moved an hour later to another room: more EXDATE values than
// moved occurrences, the ordinary case.
const seriesWithChanges = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
	"BEGIN:VEVENT\r\nUID:blob-1\r\nSUMMARY:Sync\r\nLOCATION:Room A\r\n" +
	"DTSTART;TZID=Europe/Istanbul:20260601T100000\r\nDTEND;TZID=Europe/Istanbul:20260601T103000\r\n" +
	"RRULE:FREQ=DAILY;COUNT=10\r\n" +
	"EXDATE;TZID=Europe/Istanbul:20260602T100000,20260603T100000\r\n" +
	"EXDATE;TZID=Europe/Istanbul:20260604T100000\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:blob-1\r\nRECURRENCE-ID;TZID=Europe/Istanbul:20260605T100000\r\n" +
	"SUMMARY:Sync\r\nLOCATION:Room B\r\n" +
	"DTSTART;TZID=Europe/Istanbul:20260605T110000\r\nDTEND;TZID=Europe/Istanbul:20260605T113000\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:blob-1\r\nRECURRENCE-ID;TZID=Europe/Istanbul:20260606T100000\r\nSTATUS:CANCELLED\r\n" +
	"DTSTART;TZID=Europe/Istanbul:20260606T100000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func importedPattern(t *testing.T, ical string) recurrence.AppointmentPattern {
	t.Helper()
	r := newResolver()
	msg, err := Import([]byte(ical), r.opt())
	mustNoErr(t, err, "import")
	blob, ok := msg.Props.Get(r.tag(mapi.NameAppointmentRecur, mapi.PtBinary))
	if !ok {
		t.Fatal("no recurrence blob")
	}
	p, err := recurrence.DecodeAppointment(blob.([]byte))
	mustNoErr(t, err, "decode")
	return p
}

func days(dates []uint32) []string {
	out := make([]string, len(dates))
	for i, d := range dates {
		out[i] = recurrence.WallClock(d, time.UTC).Format("01-02")
	}
	return out
}

// TestImportedSeriesCarriesItsChangedOccurrences proves the blob a MAPI client
// reads names the removed and the moved occurrences. It used to carry the RRULE
// alone, so Outlook showed every removed occurrence and the moved one at its old
// time.
func TestImportedSeriesCarriesItsChangedOccurrences(t *testing.T) {
	p := importedPattern(t, seriesWithChanges)
	if got := days(p.DeletedDates); len(got) != 5 || got[0] != "06-02" || got[3] != "06-05" || got[4] != "06-06" {
		t.Errorf("deleted days = %v, want 06-02..06-06", got)
	}
	if got := days(p.ModifiedDates); len(got) != 1 || got[0] != "06-05" {
		t.Errorf("modified days = %v, want 06-05", got)
	}
	if len(p.Exceptions) != 1 {
		t.Fatalf("exceptions = %d, want 1", len(p.Exceptions))
	}
	wantMovedRoom(t, p.Exceptions[0])
}

// wantMovedRoom checks the one moved occurrence: an hour later, in another room.
func wantMovedRoom(t *testing.T, ex recurrence.Exception) {
	t.Helper()
	at := func(m uint32) string { return recurrence.WallClock(m, time.UTC).Format("01-02 15:04") }
	if at(ex.Start) != "06-05 11:00" || at(ex.End) != "06-05 11:30" || at(ex.OriginalStart) != "06-05 10:00" {
		t.Errorf("moved occurrence = %s-%s from %s", at(ex.Start), at(ex.End), at(ex.OriginalStart))
	}
	if ex.Flags != recurrence.OverrideLocation || ex.Location != "Room B" {
		t.Errorf("override = flags %#x location %q, want the room alone", ex.Flags, ex.Location)
	}
}

// TestImportedSeriesWithoutChangesHasNoInstances is the control: a plain series
// names no deleted or modified occurrence.
func TestImportedSeriesWithoutChangesHasNoInstances(t *testing.T) {
	p := importedPattern(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:blob-2\r\nSUMMARY:x\r\n"+
		"DTSTART:20260601T080000Z\r\nDTEND:20260601T090000Z\r\nRRULE:FREQ=WEEKLY;COUNT=3\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if len(p.DeletedDates) != 0 || len(p.ModifiedDates) != 0 || len(p.Exceptions) != 0 || !p.HasTimes || p.StartTimeOffset != 480 {
		t.Errorf("plain series = %+v", p)
	}
}
