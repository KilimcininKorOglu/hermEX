package recurrence

import (
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// nMonthlyWithExceptions is the [MS-OXOCAL] "N-Monthly Recurrence BLOB with
// Exceptions" example: every third weekend day every three months, 2 PM to 5 PM,
// from 2/9/2008 for ten occurrences, with the 5/10/2008 instance moved to 5/11 and
// the 8/9/2008 instance given a new location.
const nMonthlyWithExceptions = "043004300C200300000060AE000003000000000000004100000003000000222000000A" +
	"00000000000000020000006028C50C4028C70C02000000002EC50C4028C70C8028C30C6027D50C06300000" +
	"0930000048030000FC03000002004831C50CFC31C50CA82BC50C0000882BC70C3C2CC70C882BC70C10000D" +
	"000C006E6577206C6F636174696F6E00000000040000000000000000000000040000000000000000000000" +
	"882BC70C3C2CC70C882BC70C0C006E006500770020006C006F0063006100740069006F006E000000000000" +
	"000000"

func specBlob(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(nMonthlyWithExceptions)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wall renders minutes since 1601 as the wall clock they name, for comparison.
func wall(m uint32) string {
	return WallClock(m, time.UTC).Format("2006-01-02 15:04")
}

// TestDecodeAppointmentReadsTheSpecExample decodes the published example and checks
// every field an iCalendar export reads: the instance dates, the occurrence time of
// day, and both modified instances with the one override the second carries.
func TestDecodeAppointmentReadsTheSpecExample(t *testing.T) {
	p, err := DecodeAppointment(specBlob(t))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantInstanceDates(t, p)
	if !p.HasTimes || p.StartTimeOffset != 840 || p.EndTimeOffset != 1020 {
		t.Errorf("times = %v %d %d, want 840 and 1020", p.HasTimes, p.StartTimeOffset, p.EndTimeOffset)
	}
	if len(p.Exceptions) != 2 {
		t.Fatalf("exceptions = %d, want 2", len(p.Exceptions))
	}
	wantSpecExceptions(t, p.Exceptions[0], p.Exceptions[1])
}

// wantInstanceDates checks the example's deleted and modified instance days.
func wantInstanceDates(t *testing.T, p AppointmentPattern) {
	t.Helper()
	if len(p.DeletedDates) != 2 || wall(p.DeletedDates[0]) != "2008-05-10 00:00" || wall(p.DeletedDates[1]) != "2008-08-09 00:00" {
		t.Errorf("deleted dates = %v", p.DeletedDates)
	}
	if len(p.ModifiedDates) != 2 || wall(p.ModifiedDates[0]) != "2008-05-11 00:00" {
		t.Errorf("modified dates = %v", p.ModifiedDates)
	}
}

// wantSpecExceptions checks the example's moved instance and its relocated one.
func wantSpecExceptions(t *testing.T, moved, relocated Exception) {
	t.Helper()
	if wall(moved.Start) != "2008-05-11 14:00" || wall(moved.OriginalStart) != "2008-05-10 14:00" || moved.Flags != 0 {
		t.Errorf("moved instance = %s from %s flags %#x", wall(moved.Start), wall(moved.OriginalStart), moved.Flags)
	}
	if relocated.Flags != OverrideLocation || relocated.Location != "new location" || wall(relocated.End) != "2008-08-09 17:00" {
		t.Errorf("relocated instance = flags %#x location %q end %s", relocated.Flags, relocated.Location, wall(relocated.End))
	}
}

// TestDecodeAppointmentReadsAPlainPattern keeps the blob FromRRule writes readable:
// it ends after the RecurrencePattern, so it has no time of day and no exceptions.
func TestDecodeAppointmentReadsAPlainPattern(t *testing.T) {
	blob, err := FromRRule("FREQ=WEEKLY;COUNT=4;BYDAY=MO", time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodeAppointment(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.HasTimes || p.Exceptions != nil || p.DeletedDates != nil || p.OccurrenceCount != 4 {
		t.Errorf("plain pattern decoded as %+v", p)
	}
}

// TestDecodeAppointmentRefusesACutBlob holds every truncation of the example to an
// error or a clean decode, never a panic, and refuses a cut inside the exceptions.
func TestDecodeAppointmentRefusesACutBlob(t *testing.T) {
	full := specBlob(t)
	for n := range full {
		_, _ = DecodeAppointment(full[:n]) // the property under test is that no length panics
	}
	if _, err := DecodeAppointment(full[:len(full)-30]); !errors.Is(err, errTruncated) {
		t.Errorf("cut inside the extended exceptions: err = %v, want errTruncated", err)
	}
}

// TestDecodeAppointmentRefusesAnImpossibleCount keeps a declared instance count
// from allocating more than the blob can hold.
func TestDecodeAppointmentRefusesAnImpossibleCount(t *testing.T) {
	blob := specBlob(t)
	blob[42], blob[43], blob[44], blob[45] = 0xFF, 0xFF, 0xFF, 0x7F // DeletedInstanceCount
	if _, err := DecodeAppointment(blob); err == nil {
		t.Error("a count past the end of the blob decoded")
	}
}

// TestRRuleUntilEndsAtTheLastStart ends an end-by-date rule at the instant given,
// so a series that starts after midnight keeps its last occurrence.
func TestRRuleUntilEndsAtTheLastStart(t *testing.T) {
	blob, err := FromRRule("FREQ=DAILY;UNTIL=20260610T000000Z", time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	p, err := UnmarshalBinary(blob)
	if err != nil {
		t.Fatal(err)
	}
	rule, ok := p.RRuleUntil(time.Date(2026, 6, 10, 7, 0, 0, 0, time.UTC))
	if !ok || rule != "FREQ=DAILY;INTERVAL=1;UNTIL=20260610T070000Z" {
		t.Errorf("rule = %q %v", rule, ok)
	}
	if plain, _ := p.RRuleUntil(time.Time{}); plain != "FREQ=DAILY;INTERVAL=1;UNTIL=20260610T000000Z" {
		t.Errorf("zero until changed the rule: %q", plain)
	}
}
