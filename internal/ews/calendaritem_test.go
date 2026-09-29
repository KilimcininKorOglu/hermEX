package ews

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// importAppointment stores an iCalendar event in the mailbox's calendar the way a
// CalDAV PUT stores it.
func importAppointment(t *testing.T, dir string, lines ...string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	msg, err := oxcical.Import([]byte(strings.Join(lines, "\r\n")+"\r\n"), oxcical.Options{Resolver: st.GetNamedPropIDs})
	mustNoErr(t, "import the event", err)
	_, err = st.CreateMessage(int64(mapi.PrivateFIDCalendar), msg)
	mustNoErr(t, "store the event", err)
}

// TestFindItemAndGetItemCalendarItem proves a stored appointment is served as a
// <t:CalendarItem> by FindItem on the calendar and by GetItem. The calendar was
// listed from the IMAP index, where appointments never are, so FindItem answered
// an empty calendar and every EWS client showed none of the owner's events.
func TestFindItemAndGetItemCalendarItem(t *testing.T) {
	ts, dir := seededWithMessage(t)
	importAppointment(t, dir,
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//EN",
		"BEGIN:VEVENT", "UID:ews-meeting-1", "DTSTAMP:20260101T000000Z",
		"DTSTART:20261005T090000Z", "DTEND:20261005T100000Z",
		"SUMMARY:Planning", "LOCATION:Room 4", "DESCRIPTION:agenda",
		"ORGANIZER;CN=Bob:mailto:bob@hermex.test",
		"ATTENDEE;CN=Carol;PARTSTAT=ACCEPTED;ROLE=REQ-PARTICIPANT:mailto:carol@hermex.test",
		"ATTENDEE;CN=Dan;ROLE=OPT-PARTICIPANT:mailto:dan@hermex.test",
		"END:VEVENT", "END:VCALENDAR")

	_, out := soapPost(t, ts, findItemReq("calendar"), true)
	wantContains(t, "the FindItem calendar item", out, "<CalendarItem")
	wantContains(t, "the FindItem subject", out, "Planning")
	itemID := itemIDRE.FindStringSubmatch(out)
	if len(itemID) != 2 {
		t.Fatalf("FindItem returned no ItemId: %s", out)
	}

	_, got := soapPost(t, ts, getItemReq(itemID[1]), true)
	for _, want := range []string{
		`ResponseClass="Success"`,
		"<Start>2026-10-05T09:00:00Z</Start>",
		"<End>2026-10-05T10:00:00Z</End>",
		"<IsAllDayEvent>false</IsAllDayEvent>",
		"<Location>Room 4</Location>",
		"<UID>ews-meeting-1</UID>",
		"<IsMeeting>true</IsMeeting>",
		"<CalendarItemType>Single</CalendarItemType>",
		"bob@hermex.test</EmailAddress></Mailbox></Organizer>",
		"<RequiredAttendees><Attendee><Mailbox><Name>Carol</Name><EmailAddress>carol@hermex.test",
		"<OptionalAttendees><Attendee><Mailbox><Name>Dan</Name><EmailAddress>dan@hermex.test",
		"agenda",
	} {
		wantContains(t, "the GetItem calendar item", got, want)
	}
}

// TestCalendarItemCarriesItsAllDayFlag proves IsAllDayEvent is written for every
// calendar item, true for an all-day event and false for any other. A client
// that finds the element absent (eM Client) drops the item from its calendar.
func TestCalendarItemCarriesItsAllDayFlag(t *testing.T) {
	ts, dir := seededWithMessage(t)
	importAppointment(t, dir,
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//EN",
		"BEGIN:VEVENT", "UID:ews-allday-1", "DTSTAMP:20260101T000000Z",
		"DTSTART;VALUE=DATE:20261006", "DTEND;VALUE=DATE:20261007", "SUMMARY:Holiday",
		"END:VEVENT", "END:VCALENDAR")

	_, out := soapPost(t, ts, findItemReq("calendar"), true)
	wantContains(t, "the all-day flag", out, "<IsAllDayEvent>true</IsAllDayEvent>")
}
