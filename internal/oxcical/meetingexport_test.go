package oxcical

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
	"hermex/internal/recurrence"
)

// The [MS-OXCICAL] "Organizer's Cancellation of an Instance" example: the global
// object id Outlook stamps on the cancellation of the 5/28/2008 occurrence, and the
// UID Outlook exports for it.
const (
	specInstanceGOID = "040000008200E00074C5B7101A82E00807D8051C3046642B576AC801000000000000000010000000622C639E40D09342B747A1672730CBBA"
	specUID          = "040000008200E00074C5B7101A82E008000000003046642B576AC801000000000000000010000000622C639E40D09342B747A1672730CBBA"
)

// The [MS-OXOCAL] "N-Monthly Recurrence BLOB with Exceptions" example: every third
// weekend day every three months, 2 PM to 5 PM, from 2/9/2008 for ten occurrences,
// the 5/10/2008 instance moved to 5/11 and the 8/9/2008 one given a new location.
const specSeriesBlob = "043004300C200300000060AE000003000000000000004100000003000000222000000A" +
	"00000000000000020000006028C50C4028C70C02000000002EC50C4028C70C8028C30C6027D50C06300000" +
	"0930000048030000FC03000002004831C50CFC31C50CA82BC50C0000882BC70C3C2CC70C882BC70C10000D" +
	"000C006E6577206C6F636174696F6E00000000040000000000000000000000040000000000000000000000" +
	"882BC70C3C2CC70C882BC70C0C006E006500770020006C006F0063006100740069006F006E000000000000" +
	"000000"

// meetingResolver is a resolver holding every named property a MAPI client writes
// on a meeting, allocated before a test builds one.
func meetingResolver() *resolver {
	r := newResolver()
	_, _ = r.resolve(true, []mapi.PropertyName{mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole,
		mapi.NameGlobalObjectId, mapi.NameExceptionReplaceTime, mapi.NameAppointmentRecur, mapi.NameAppointmentSubType,
		mapi.NameAppointmentTimeZoneDefRecur, mapi.NameAppointmentTimeZoneDefStartDisplay, mapi.NameAppointmentLocation})
	return r
}

// outlookMeeting builds a meeting object the way a MAPI client stores one: the
// organizer as the representing identity and one attendee.
func outlookMeeting(r *resolver, class string, start, end time.Time, extra ...mapi.TaggedPropVal) *oxcmail.Message {
	props := mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: class},
		{Tag: mapi.PrSubject, Value: "Status"},
		{Tag: r.tag(mapi.NameAppointmentStartWhole, mapi.PtSysTime), Value: mapi.UnixToNTTime(start)},
		{Tag: r.tag(mapi.NameAppointmentEndWhole, mapi.PtSysTime), Value: mapi.UnixToNTTime(end)},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "alice@hermex.test"},
	}
	props = append(props, extra...)
	return &oxcmail.Message{Props: props, Recipients: []mapi.PropertyValues{{
		{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
		{Tag: mapi.PrSmtpAddress, Value: "bob@hermex.test"},
	}}}
}

// zoneDef is the time zone definition a MAPI client stores for loc.
func zoneDef(r *resolver, name mapi.PropertyName, loc *time.Location, at time.Time) mapi.TaggedPropVal {
	return mapi.TaggedPropVal{Tag: r.tag(name, mapi.PtBinary), Value: tzDefinitionBlob(zoneKeyName(loc), zoneReg(loc, at), tzRuleEffective)}
}

func exportString(t *testing.T, msg *oxcmail.Message, r *resolver) string {
	t.Helper()
	out, err := Export(msg, r.opt())
	if err != nil {
		t.Fatal(err)
	}
	return string(unfoldLines(out))
}

// unfoldLines joins folded content lines so an assertion can match a long value.
func unfoldLines(b []byte) []byte {
	return []byte(strings.ReplaceAll(string(b), "\r\n ", ""))
}

func wantLines(t *testing.T, s string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(s, l+"\r\n") {
			t.Errorf("export missing %q\n%s", l, s)
		}
	}
}

// TestExportOutlookRequestIsAnITIPRequest renders a meeting request a MAPI client
// submitted: it carries METHOD:REQUEST and the UID its global object id exports as.
// Without either, the request reached an outside attendee as a plain mail.
func TestExportOutlookRequestIsAnITIPRequest(t *testing.T) {
	r := meetingResolver()
	goid := mustHex(t, specUID)
	start := time.Date(2026, 7, 1, 14, 0, 0, 0, time.UTC)
	msg := outlookMeeting(r, "IPM.Schedule.Meeting.Request", start, start.Add(time.Hour),
		mapi.TaggedPropVal{Tag: r.tag(mapi.NameGlobalObjectId, mapi.PtBinary), Value: goid})
	s := exportString(t, msg, r)
	wantLines(t, s, "METHOD:REQUEST", "UID:"+specUID, "DTSTART:20260701T140000Z",
		"ORGANIZER:mailto:alice@hermex.test", "ATTENDEE:mailto:bob@hermex.test")
	if strings.Contains(s, "RECURRENCE-ID") || strings.Contains(s, "VTIMEZONE") {
		t.Errorf("a whole-meeting request carries an instance or a zone:\n%s", s)
	}
}

// TestExportInstanceCancellationNamesItsOccurrence reproduces the [MS-OXCICAL]
// instance cancellation: the date in the global object id and the wall-clock start
// make the RECURRENCE-ID, the UID drops the date, and the zone rides along.
func TestExportInstanceCancellationNamesItsOccurrence(t *testing.T) {
	r := meetingResolver()
	pacific := mustZone(t, "America/Los_Angeles")
	start := time.Date(2008, 5, 28, 21, 0, 0, 0, time.UTC)
	goid := mapi.TaggedPropVal{Tag: 0, Value: mustHex(t, specInstanceGOID)}
	msg := outlookMeeting(r, "IPM.Schedule.Meeting.Canceled", start, start.Add(30*time.Minute))
	goid.Tag = r.tag(mapi.NameGlobalObjectId, mapi.PtBinary)
	msg.Props = append(msg.Props, goid, zoneDef(r, mapi.NameAppointmentTimeZoneDefStartDisplay, pacific, start))

	s := exportString(t, msg, r)
	wantLines(t, s, "METHOD:CANCEL", "UID:"+specUID, "STATUS:CANCELLED", "TZID:America/Los_Angeles",
		"RECURRENCE-ID;TZID=America/Los_Angeles:20080528T140000", "DTSTART;TZID=America/Los_Angeles:20080528T140000")

	back, err := Import([]byte(s), r.opt())
	if err != nil {
		t.Fatal(err)
	}
	if class := getStr(&back.Props, mapi.PrMessageClass); class != "IPM.Schedule.Meeting.Canceled" {
		t.Errorf("the exported cancellation imports as %q", class)
	}
}

// TestExportInstanceWithoutAZoneStaysUTC keeps an instance whose zone is unknown
// exact: the RECURRENCE-ID is the instant, written in UTC.
func TestExportInstanceWithoutAZoneStaysUTC(t *testing.T) {
	r := meetingResolver()
	start := time.Date(2008, 5, 28, 21, 0, 0, 0, time.UTC)
	msg := outlookMeeting(r, "IPM.Schedule.Meeting.Canceled", start, start.Add(30*time.Minute))
	msg.Props = append(msg.Props, mapi.TaggedPropVal{Tag: r.tag(mapi.NameGlobalObjectId, mapi.PtBinary), Value: mustHex(t, specInstanceGOID)})
	s := exportString(t, msg, r)
	wantLines(t, s, "RECURRENCE-ID:20080528T210000Z", "DTSTART:20080528T210000Z")
	if strings.Contains(s, "VTIMEZONE") {
		t.Errorf("an unzoned instance carries a VTIMEZONE:\n%s", s)
	}
}

// TestExportReplaceTimeWins takes the RECURRENCE-ID from the replace time when it is
// stored, as [MS-OXCICAL] orders the two sources.
func TestExportReplaceTimeWins(t *testing.T) {
	r := meetingResolver()
	start := time.Date(2008, 5, 29, 18, 0, 0, 0, time.UTC)
	msg := outlookMeeting(r, "IPM.Schedule.Meeting.Request", start, start.Add(time.Hour))
	msg.Props = append(msg.Props,
		mapi.TaggedPropVal{Tag: r.tag(mapi.NameGlobalObjectId, mapi.PtBinary), Value: mustHex(t, specInstanceGOID)},
		mapi.TaggedPropVal{Tag: r.tag(mapi.NameExceptionReplaceTime, mapi.PtSysTime), Value: mapi.UnixToNTTime(time.Date(2008, 5, 28, 21, 0, 0, 0, time.UTC))})
	wantLines(t, exportString(t, msg, r), "RECURRENCE-ID:20080528T210000Z", "DTSTART:20080529T180000Z")
}

// TestExportForeignUIDRoundTrips unwraps a UID another calendar system chose, which
// a global object id carries behind the vCal-Uid marker.
func TestExportForeignUIDRoundTrips(t *testing.T) {
	uid := "event-7@calendar.example"
	goid := make([]byte, goidDataOffset, goidDataOffset+len(vcalUIDMarker)+len(uid))
	copy(goid, goidClassID)
	binary.LittleEndian.PutUint32(goid[goidSizeOffset:], uint32(len(vcalUIDMarker)+len(uid)))
	goid = append(append(goid, vcalUIDMarker...), uid...)
	if got := globalObjectUID(goid); got != uid {
		t.Errorf("UID = %q, want %q", got, uid)
	}
	if got := globalObjectUID(goid[:20]); got != "" {
		t.Errorf("a cut id exported UID %q", got)
	}
	if got := GlobalObjectID(uid); !bytes.Equal(got, goid[:len(got)]) || len(got) != len(goid) {
		t.Errorf("GlobalObjectID(%q) = %X, want %X", uid, got, goid)
	}
}

// TestGlobalObjectIDInvertsTheUID maps an exported UID back to the id of the whole
// meeting, clearing the instance date an occurrence's id carries.
func TestGlobalObjectIDInvertsTheUID(t *testing.T) {
	if got := globalObjectUID(GlobalObjectID(specUID)); got != specUID {
		t.Errorf("round trip = %q, want %q", got, specUID)
	}
	fromInstance := GlobalObjectID(strings.ToLower(specInstanceGOID))
	if !bytes.Equal(fromInstance, mustHex(t, specUID)) {
		t.Errorf("an instance id maps to %X, want the whole meeting's", fromInstance)
	}
	if GlobalObjectID("") != nil {
		t.Error("an empty UID names an id")
	}
}

// TestExportOutlookSeries renders the [MS-OXOCAL] example series: its RRULE, the two
// modified occurrences as overrides, and no EXDATE on the days they came from, so an
// expansion shows the moved occurrence once, at its new time.
func TestExportOutlookSeries(t *testing.T) {
	r := meetingResolver()
	pacific := mustZone(t, "America/Los_Angeles")
	start := time.Date(2008, 2, 9, 14, 0, 0, 0, pacific)
	msg := outlookMeeting(r, "IPM.Schedule.Meeting.Request", start, start.Add(3*time.Hour),
		mapi.TaggedPropVal{Tag: r.tag(mapi.NameAppointmentRecur, mapi.PtBinary), Value: mustHex(t, specSeriesBlob)},
		mapi.TaggedPropVal{Tag: r.tag(mapi.NameAppointmentLocation, mapi.PtUnicode), Value: "Room 1"})
	msg.Props = append(msg.Props, zoneDef(r, mapi.NameAppointmentTimeZoneDefRecur, pacific, start))

	s := exportString(t, msg, r)
	wantLines(t, s, "METHOD:REQUEST", "DTSTART;TZID=America/Los_Angeles:20080209T140000",
		"RRULE:FREQ=MONTHLY;INTERVAL=3;COUNT=10;BYDAY=SU,SA;BYSETPOS=3",
		"RECURRENCE-ID;TZID=America/Los_Angeles:20080510T140000", "DTSTART;TZID=America/Los_Angeles:20080511T140000",
		"RECURRENCE-ID;TZID=America/Los_Angeles:20080809T140000", "LOCATION:new location")
	if strings.Contains(s, "EXDATE") {
		t.Errorf("a modified occurrence's day is excluded:\n%s", s)
	}
}

// TestExportSeriesExcludesADeletedDay excludes a deleted occurrence at the series'
// own time of day, read from the start when the pattern records none.
func TestExportSeriesExcludesADeletedDay(t *testing.T) {
	r := meetingResolver()
	istanbul := mustZone(t, "Europe/Istanbul")
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, istanbul)
	blob := weeklyWithDeletedDay(t, start, time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC))
	msg := outlookMeeting(r, "IPM.Appointment", start, start.Add(time.Hour),
		mapi.TaggedPropVal{Tag: r.tag(mapi.NameAppointmentRecur, mapi.PtBinary), Value: blob})
	msg.Props = append(msg.Props, zoneDef(r, mapi.NameAppointmentTimeZoneDefRecur, istanbul, start))

	s := exportString(t, msg, r)
	wantLines(t, s, "RRULE:FREQ=WEEKLY;INTERVAL=1;COUNT=4;BYDAY=MO", "EXDATE;TZID=Europe/Istanbul:20260608T090000")
	if strings.Contains(s, "METHOD:") {
		t.Errorf("a plain appointment carries a METHOD:\n%s", s)
	}

	spans, ok := OccurrencesIn([]byte(s), start, start.AddDate(0, 1, 0))
	if !ok {
		t.Fatal("the exported series does not expand")
	}
	var got []string
	for _, sp := range spans {
		got = append(got, sp.Start.In(istanbul).Format("01-02 15:04"))
	}
	if strings.Join(got, ",") != "06-01 09:00,06-15 09:00,06-22 09:00" {
		t.Errorf("instances = %v, want every Monday but the deleted 06-08", got)
	}
}

// weeklyWithDeletedDay is a four-Monday pattern with one deleted occurrence, the
// shape a MAPI client writes after deleting one instance.
func weeklyWithDeletedDay(t *testing.T, start, deleted time.Time) []byte {
	t.Helper()
	blob, err := recurrence.FromRRule("FREQ=WEEKLY;COUNT=4;BYDAY=MO", start)
	if err != nil {
		t.Fatal(err)
	}
	const deletedCountAt = 38 // header 22, DayOfWeek 4, EndType, OccurrenceCount, FirstDOW
	day := uint32((deleted.Unix() + 11644473600) / 60)
	out := append([]byte(nil), blob[:deletedCountAt]...)
	out = binary.LittleEndian.AppendUint32(out, 1)
	out = binary.LittleEndian.AppendUint32(out, day)
	return append(out, blob[deletedCountAt+4:]...)
}

// TestExportAllDayOnItsLocalDate writes an all-day event stored as local midnight on
// the date it falls on in its zone, not on the UTC date before it.
func TestExportAllDayOnItsLocalDate(t *testing.T) {
	r := meetingResolver()
	istanbul := mustZone(t, "Europe/Istanbul")
	start := time.Date(2026, 6, 12, 0, 0, 0, 0, istanbul)
	msg := outlookMeeting(r, "IPM.Appointment", start, start.AddDate(0, 0, 1),
		mapi.TaggedPropVal{Tag: r.tag(mapi.NameAppointmentSubType, mapi.PtBoolean), Value: true})
	msg.Props = append(msg.Props, zoneDef(r, mapi.NameAppointmentTimeZoneDefStartDisplay, istanbul, start))
	wantLines(t, exportString(t, msg, r), "DTSTART;VALUE=DATE:20260612", "DTEND;VALUE=DATE:20260613")
}

// TestExportVerbatimRequestKeepsItsBody sends a preserved whole-meeting request as
// it was, with METHOD:REQUEST, and synthesizes an instance message instead, whose
// preserved body would describe the whole series.
func TestExportVerbatimRequestKeepsItsBody(t *testing.T) {
	r := meetingResolver()
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:rec-1\r\nDTSTART:20260601T090000Z\r\n" +
		"RRULE:FREQ=WEEKLY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	start := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
	msg := outlookMeeting(r, "IPM.Schedule.Meeting.Request", start, start.Add(time.Hour),
		mapi.TaggedPropVal{Tag: mapi.PrIcalOriginal, Value: []byte(body)})
	wantLines(t, exportString(t, msg, r), "METHOD:REQUEST", "RRULE:FREQ=WEEKLY")

	msg.Props = append(msg.Props, mapi.TaggedPropVal{Tag: r.tag(mapi.NameExceptionReplaceTime, mapi.PtSysTime), Value: mapi.UnixToNTTime(start)})
	s := exportString(t, msg, r)
	wantLines(t, s, "METHOD:REQUEST", "RECURRENCE-ID:20260608T090000Z")
	if strings.Contains(s, "RRULE") {
		t.Errorf("an instance request carries the series rule:\n%s", s)
	}
}
