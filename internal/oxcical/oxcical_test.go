package oxcical

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// resolver is a deterministic stand-in for the store's named-property allocator:
// stable ids >= 0x8000 keyed by the property name. The SAME instance must drive
// Import and Export so resolved proptags match across a round trip.
type resolver struct {
	ids  map[string]uint16
	next uint16
}

func newResolver() *resolver { return &resolver{ids: map[string]uint16{}, next: 0x8000} }

func nameKey(n mapi.PropertyName) string {
	return fmt.Sprintf("%v|%d|%s", n.GUID, n.LID, n.Name)
}

func (r *resolver) resolve(create bool, names []mapi.PropertyName) ([]uint16, error) {
	out := make([]uint16, len(names))
	for i, n := range names {
		k := nameKey(n)
		id, ok := r.ids[k]
		if !ok {
			if !create {
				continue
			}
			id = r.next
			r.next++
			r.ids[k] = id
		}
		out[i] = id
	}
	return out, nil
}

func (r *resolver) opt() Options { return Options{Resolver: r.resolve} }

func (r *resolver) tag(name mapi.PropertyName, typ mapi.PropType) mapi.PropTag {
	ids, _ := r.resolve(false, []mapi.PropertyName{name})
	if ids[0] == 0 {
		return 0
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(typ))
}

func (r *resolver) timeVal(m *oxcmail.Message, name mapi.PropertyName) (uint64, bool) {
	v, ok := m.Props.Get(r.tag(name, mapi.PtSysTime))
	if !ok {
		return 0, false
	}
	nt, ok := v.(uint64)
	return nt, ok
}

func (r *resolver) boolVal(m *oxcmail.Message, name mapi.PropertyName) bool {
	v, ok := m.Props.Get(r.tag(name, mapi.PtBoolean))
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

func (r *resolver) longVal(m *oxcmail.Message, name mapi.PropertyName) (int32, bool) {
	v, ok := m.Props.Get(r.tag(name, mapi.PtLong))
	if !ok {
		return 0, false
	}
	n, ok := v.(int32)
	return n, ok
}

func (r *resolver) strVal(m *oxcmail.Message, name mapi.PropertyName) string {
	v, ok := m.Props.Get(r.tag(name, mapi.PtUnicode))
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func str(m *oxcmail.Message, tag mapi.PropTag) string {
	if v, ok := m.Props.Get(tag); ok {
		s, _ := v.(string)
		return s
	}
	return ""
}

const timedICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:ev-1\r\n" +
	"SUMMARY:Standup\r\nDESCRIPTION:Daily sync\r\nLOCATION:Room 5\r\n" +
	"DTSTART:20260612T090000Z\r\nDTEND:20260612T093000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// TestImportTimed checks that a timed VEVENT lands in the right MAPI properties
// (intent: a wrong tag would hide the field from every calendar client).
func TestImportTimed(t *testing.T) {
	r := newResolver()
	msg, err := Import([]byte(timedICS), r.opt())
	if err != nil {
		t.Fatal(err)
	}
	if got := str(msg, mapi.PrMessageClass); got != "IPM.Appointment" {
		t.Errorf("message class %q, want IPM.Appointment", got)
	}
	if got := str(msg, mapi.PrSubject); got != "Standup" {
		t.Errorf("subject %q, want Standup", got)
	}
	if got := str(msg, mapi.PrBody); got != "Daily sync" {
		t.Errorf("body %q, want Daily sync", got)
	}
	if got := r.strVal(msg, mapi.NameAppointmentLocation); got != "Room 5" {
		t.Errorf("location %q, want Room 5", got)
	}
	wantStart := mapi.UnixToNTTime(time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC))
	if got, ok := r.timeVal(msg, mapi.NameAppointmentStartWhole); !ok || got != wantStart {
		t.Errorf("start %d (ok=%v), want %d", got, ok, wantStart)
	}
	wantEnd := mapi.UnixToNTTime(time.Date(2026, 6, 12, 9, 30, 0, 0, time.UTC))
	if got, ok := r.timeVal(msg, mapi.NameAppointmentEndWhole); !ok || got != wantEnd {
		t.Errorf("end %d (ok=%v), want %d", got, ok, wantEnd)
	}
}

// TestRoundTripTimed exports an imported event and re-parses it, confirming the
// core fields survive synthesis.
func TestRoundTripTimed(t *testing.T) {
	r := newResolver()
	msg, err := Import([]byte(timedICS), r.opt())
	if err != nil {
		t.Fatal(err)
	}
	out, err := Export(msg, r.opt())
	if err != nil {
		t.Fatal(err)
	}
	cal, err := parseICal(out)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	vev := cal.sub("VEVENT")
	if vev == nil {
		t.Fatalf("no VEVENT in export\n%s", out)
	}
	checks := map[string]string{
		"UID": "ev-1", "SUMMARY": "Standup", "DESCRIPTION": "Daily sync", "LOCATION": "Room 5",
	}
	for name, want := range checks {
		if got := vev.propText(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if l := vev.prop("DTSTART"); l == nil || l.value != "20260612T090000Z" {
		t.Errorf("DTSTART = %v, want 20260612T090000Z", l)
	}
	if l := vev.prop("DTEND"); l == nil || l.value != "20260612T093000Z" {
		t.Errorf("DTEND = %v, want 20260612T093000Z", l)
	}
}

// TestTimezoneToUTC is the tzdata landmine guard: a TZID-bearing local time must
// resolve to the correct UTC instant via time.LoadLocation (which needs the
// embedded zoneinfo to work in the distroless runtime). Europe/Istanbul is UTC+3,
// so 12:00 local is 09:00Z.
func TestTimezoneToUTC(t *testing.T) {
	const tzICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:ev-tz\r\nSUMMARY:TZ\r\n" +
		"DTSTART;TZID=Europe/Istanbul:20260612T120000\r\nDTEND;TZID=Europe/Istanbul:20260612T130000\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	r := newResolver()
	msg, err := Import([]byte(tzICS), r.opt())
	if err != nil {
		t.Fatal(err)
	}
	want := mapi.UnixToNTTime(time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC))
	if got, ok := r.timeVal(msg, mapi.NameAppointmentStartWhole); !ok || got != want {
		t.Fatalf("TZID start %d (ok=%v), want %d (09:00Z) — is time/tzdata linked?", got, ok, want)
	}
	out, err := Export(msg, r.opt())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "DTSTART:20260612T090000Z") {
		t.Errorf("export did not normalize TZID start to UTC Z\n%s", out)
	}
}

// TestAllDay confirms a VALUE=DATE event is marked all-day and exports as a date.
func TestAllDay(t *testing.T) {
	const allDayICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:ev-ad\r\nSUMMARY:Holiday\r\n" +
		"DTSTART;VALUE=DATE:20260612\r\nDTEND;VALUE=DATE:20260613\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	r := newResolver()
	msg, err := Import([]byte(allDayICS), r.opt())
	if err != nil {
		t.Fatal(err)
	}
	if !r.boolVal(msg, mapi.NameAppointmentSubType) {
		t.Error("all-day event did not set AppointmentSubType")
	}
	out, err := Export(msg, r.opt())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "DTSTART;VALUE=DATE:20260612") {
		t.Errorf("all-day export not a DATE value\n%s", out)
	}
}

// TestRecurringVerbatim confirms a recurring event is preserved byte-for-byte and
// re-served unchanged, not synthesized (v1 cannot build the binary pattern).
func TestRecurringVerbatim(t *testing.T) {
	const recICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:r-1\r\nSUMMARY:Weekly\r\n" +
		"DTSTART:20260612T090000Z\r\nRRULE:FREQ=WEEKLY;BYDAY=FR\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	r := newResolver()
	msg, err := Import([]byte(recICS), r.opt())
	if err != nil {
		t.Fatal(err)
	}
	v, ok := msg.Props.Get(mapi.PrIcalOriginal)
	if !ok {
		t.Fatal("recurring event did not set PrIcalOriginal")
	}
	if raw, _ := v.([]byte); string(raw) != recICS {
		t.Errorf("PrIcalOriginal not the verbatim source")
	}
	if got := str(msg, mapi.PrSubject); got != "Weekly" {
		t.Errorf("minimal subject %q, want Weekly", got)
	}
	out, err := Export(msg, r.opt())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != recICS {
		t.Errorf("recurring export not verbatim:\n%s", out)
	}
}

// TestImportSemantics pins the PRIORITY/CLASS/TRANSP/VALARM mappings (intent: a
// wrong mapping would silently change an event's importance, privacy, or alarm).
func TestImportSemantics(t *testing.T) {
	const ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:ev-s\r\nSUMMARY:Sensitive\r\n" +
		"DTSTART:20260612T090000Z\r\nPRIORITY:1\r\nCLASS:PRIVATE\r\nTRANSP:TRANSPARENT\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	r := newResolver()
	msg, err := Import([]byte(ics), r.opt())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := msg.Props.Get(mapi.PrImportance); v != int32(mapi.ImportanceHigh) {
		t.Errorf("PRIORITY:1 importance %v, want High", v)
	}
	if v, _ := msg.Props.Get(mapi.PrSensitivity); v != int32(mapi.SensitivityPrivate) {
		t.Errorf("CLASS:PRIVATE sensitivity %v, want Private", v)
	}
	if got, ok := r.longVal(msg, mapi.NameBusyStatus); !ok || got != busyFree {
		t.Errorf("TRANSP:TRANSPARENT busy %d (ok=%v), want free", got, ok)
	}
	if !r.boolVal(msg, mapi.NameReminderSet) {
		t.Error("VALARM did not set ReminderSet")
	}
	if got, ok := r.longVal(msg, mapi.NameReminderDelta); !ok || got != 15 {
		t.Errorf("reminder delta %d (ok=%v), want 15", got, ok)
	}
}

// TestNoEvent rejects a calendar with no VEVENT.
func TestNoEvent(t *testing.T) {
	const noev = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n"
	if _, err := Import([]byte(noev), newResolver().opt()); !errors.Is(err, errNoEvent) {
		t.Errorf("err = %v, want errNoEvent", err)
	}
}
