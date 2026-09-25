package oxcical

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"
	"unicode/utf16"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
	"hermex/internal/recurrence"
)

// TestZoneRegMatchesTheWindowsPacificRule derives the Pacific rule from the Go
// time zone data and compares it with the values Windows keeps for "Pacific
// Standard Time": bias 480, daylight bias -60, standard time from 02:00 on the
// first Sunday of November, daylight time from 02:00 on the second Sunday of
// March.
func TestZoneRegMatchesTheWindowsPacificRule(t *testing.T) {
	got := zoneReg(mustZone(t, "America/Los_Angeles"), time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	want := tzReg{
		bias:         480,
		daylightBias: -60,
		standardDate: systemTime{month: 11, dayOfWeek: 0, day: 1, hour: 2},
		daylightDate: systemTime{month: 3, dayOfWeek: 0, day: 2, hour: 2},
	}
	if got != want {
		t.Fatalf("zoneReg = %+v, want %+v", got, want)
	}
	if last := zoneReg(mustZone(t, "Europe/Berlin"), time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)); last.standardDate.day != 5 || last.daylightDate.day != 5 {
		t.Errorf("Berlin switches on the last Sunday, want week 5: %+v", last)
	}
	if fixed := zoneReg(mustZone(t, "Europe/Istanbul"), time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)); fixed != (tzReg{bias: -180}) {
		t.Errorf("Istanbul rule = %+v, want bias -180 and no switch", fixed)
	}
}

// TestTZDefinitionHeaderMatchesTheSpecSample pins the TZDEFINITION header byte
// for byte against the [MS-OXOCAL] example for "Pacific Standard Time": version
// 2.1, a 48-byte header, the KeyName flag, 21 characters of name. The rule
// follows with its 2.1 version, its 62-byte size and the given flags.
func TestTZDefinitionHeaderMatchesTheSpecSample(t *testing.T) {
	blob := tzDefinitionBlob("Pacific Standard Time", tzReg{bias: 480}, tzRuleEffective|tzRuleRecurCurrent)
	name := make([]byte, 0, 42)
	for _, u := range utf16.Encode([]rune("Pacific Standard Time")) {
		name = binary.LittleEndian.AppendUint16(name, u)
	}
	want := append(append(mustHex(t, "0201300002001500"), name...), mustHex(t, "0100"+"02013E00"+"0300"+"0100")...)
	if !bytes.HasPrefix(blob, want) {
		t.Fatalf("header = %x\nwant    %x", blob[:len(want)], want)
	}
	if len(blob) != len(want)+14+12+32 {
		t.Errorf("blob is %d bytes, want %d", len(blob), len(want)+14+12+32)
	}
	if got := len(tzStructBlob(tzReg{})); got != 48 {
		t.Errorf("TZREG is %d bytes, want 48", got)
	}
}

// TestImportWritesOutlookZones covers what the import stores: a timed event in a
// named zone gets the two display definitions, marked effective; a series also
// gets the TZREG, its description and the recurring definition, marked
// effective and recurring; and the series pattern is laid out on the zone's wall
// clock, 09:00, not on UTC.
func TestImportWritesOutlookZones(t *testing.T) {
	const series = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:tz-1\r\nSUMMARY:Standup\r\n" +
		"DTSTART;TZID=Europe/Berlin:20260612T090000\r\nDTEND;TZID=Europe/Berlin:20260612T093000\r\n" +
		"RRULE:FREQ=DAILY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	r := newResolver()
	msg, err := Import([]byte(series), r.opt())
	mustNoErr(t, err, "import")

	if got := ruleFlags(t, r, msg, mapi.NameAppointmentTimeZoneDefStartDisplay); got != tzRuleEffective {
		t.Errorf("StartDisplay rule flags = %#x, want 0x0002", got)
	}
	if got := ruleFlags(t, r, msg, mapi.NameAppointmentTimeZoneDefRecur); got != tzRuleEffective|tzRuleRecurCurrent {
		t.Errorf("Recur rule flags = %#x, want 0x0003", got)
	}
	if v, _ := msg.Props.Get(r.tag(mapi.NameTimeZoneDescription, mapi.PtUnicode)); v != "W. Europe Standard Time" {
		t.Errorf("TimeZoneDescription = %v, want W. Europe Standard Time", v)
	}
	if v, ok := msg.Props.Get(r.tag(mapi.NameTimeZoneStruct, mapi.PtBinary)); !ok || len(v.([]byte)) != 48 {
		t.Errorf("TimeZoneStruct = %v, want a 48-byte TZREG", v)
	}
	blob, _ := msg.Props.Get(r.tag(mapi.NameAppointmentRecur, mapi.PtBinary))
	pattern, err := recurrence.UnmarshalBinary(blob.([]byte))
	mustNoErr(t, err, "decode pattern")
	if got := pattern.StartDate % (24 * 60); got != 9*60 {
		t.Errorf("pattern start is %d minutes past midnight, want 540 (09:00 Berlin)", got)
	}
}

// TestImportWritesNoZoneForUnzonedTimes keeps the properties off events that name
// no zone: a UTC time, and an all-day date.
func TestImportWritesNoZoneForUnzonedTimes(t *testing.T) {
	for _, dt := range []string{"DTSTART:20260612T090000Z", "DTSTART;VALUE=DATE:20260612"} {
		body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:tz-2\r\nSUMMARY:x\r\n" + dt + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
		r := newResolver()
		msg, err := Import([]byte(body), r.opt())
		mustNoErr(t, err, "import")
		if _, ok := msg.Props.Get(r.tag(mapi.NameAppointmentTimeZoneDefStartDisplay, mapi.PtBinary)); ok {
			t.Errorf("%s got a display zone", dt)
		}
	}
}

// ruleFlags reads the flags of the one rule in a stored time zone definition.
func ruleFlags(t *testing.T, r *resolver, msg *oxcmail.Message, name mapi.PropertyName) uint16 {
	t.Helper()
	v, ok := msg.Props.Get(r.tag(name, mapi.PtBinary))
	if !ok {
		t.Fatalf("%v not stored", name)
	}
	b := v.([]byte)
	header := 4 + int(binary.LittleEndian.Uint16(b[2:4]))
	return binary.LittleEndian.Uint16(b[header+4 : header+6])
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
