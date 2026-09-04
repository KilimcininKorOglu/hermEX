package oxcical

import (
	"testing"

	"hermex/internal/mapi"
)

// TestSequenceRejectsWiderThanPtLong pins the parse width of the iCalendar
// SEQUENCE property. It lands in a 32-bit named long, so a wider integer parsed at
// the platform width wrapped: 2147483648 became -2147483648, which reads as an
// older revision than every real one and makes a stale update win.
func TestSequenceRejectsWiderThanPtLong(t *testing.T) {
	const head = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:seq-1\r\n" +
		"DTSTART:20260601T090000Z\r\nDTEND:20260601T093000Z\r\nSUMMARY:Sync\r\n"

	cases := []struct {
		name string
		in   string
		want int32 // 0 with wantSet false means the property must be absent
		set  bool
	}{
		{"in range", "7", 7, true},
		{"above int32", "2147483648", 0, false},
		{"below int32", "-2147483649", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newResolver()
			msg, err := Import([]byte(head+"SEQUENCE:"+tc.in+"\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"), r.opt())
			if err != nil {
				t.Fatal(err)
			}
			got, ok := r.longVal(msg, mapi.NameAppointmentSequence)
			if !tc.set {
				if ok {
					t.Fatalf("SEQUENCE %q was accepted as %d", tc.in, got)
				}
				return
			}
			if !ok {
				t.Fatalf("SEQUENCE %q was dropped", tc.in)
			}
			if got != tc.want {
				t.Errorf("SEQUENCE %q = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
