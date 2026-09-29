package ews

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// createCalendarReq is a CreateItem of one CalendarItem with the given
// SendMeetingInvitations value ("" leaves the attribute out) and item body.
func createCalendarReq(invitations, item string) string {
	attr := ""
	if invitations != "" {
		attr = ` SendMeetingInvitations="` + invitations + `"`
	}
	return wrapRequest(`<CreateItem xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `"` + attr + `>` +
		`<Items><t:CalendarItem>` + item + `</t:CalendarItem></Items></CreateItem>`)
}

// weeklyMeeting is a weekly meeting with bob on Mondays at 09:00 Berlin time,
// three times.
const weeklyMeeting = `<t:Subject>Planning</t:Subject>` +
	`<t:Body BodyType="Text">agenda</t:Body>` +
	`<t:Start>2026-10-05T07:00:00Z</t:Start><t:End>2026-10-05T08:00:00Z</t:End>` +
	`<t:LegacyFreeBusyStatus>Tentative</t:LegacyFreeBusyStatus>` +
	`<t:Location>Room 4</t:Location>` +
	`<t:RequiredAttendees><t:Attendee><t:Mailbox><t:Name>Bob</t:Name><t:EmailAddress>bob@hermex.test</t:EmailAddress></t:Mailbox></t:Attendee></t:RequiredAttendees>` +
	`<t:Recurrence><t:WeeklyRecurrence><t:Interval>1</t:Interval><t:DaysOfWeek>Monday</t:DaysOfWeek></t:WeeklyRecurrence>` +
	`<t:NumberedRecurrence><t:StartDate>2026-10-05</t:StartDate><t:NumberOfOccurrences>3</t:NumberOfOccurrences></t:NumberedRecurrence></t:Recurrence>` +
	`<t:StartTimeZone Id="W. Europe Standard Time"/>`

// mailCount returns how many messages a mailbox folder holds.
func mailCount(t *testing.T, path string, fid int64) int {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	msgs, err := st.ListMessages(fid)
	mustNoErr(t, "list the folder", err)
	return len(msgs)
}

// TestCreateItemStoresACalendarItem proves a CalendarItem created over EWS is the
// one appointment every protocol reads: a weekly series in its own time zone that
// CalDAV exports with its rule and free/busy expands, with its busy status, and
// that its attendee receives the invitation. CreateItem dropped the CalendarItem
// element and answered with nothing, so an EWS client could not add an event.
func TestCreateItemStoresACalendarItem(t *testing.T) {
	ts, paths := availabilityServer(t)
	_, out := soapPost(t, ts, createCalendarReq("SendToAllAndSaveCopy", weeklyMeeting), true)
	wantContains(t, "the create", out, `ResponseClass="Success"`)
	wantContains(t, "the created item", out, "<CalendarItem")

	st, err := objectstore.Open(paths["alice@hermex.test"])
	mustNoErr(t, "open alice", err)
	msg, err := st.OpenMessage(calendarObjectID(t, st))
	mustNoErr(t, "open the appointment", err)
	ical, err := oxcical.Export(msg, oxcical.Options{Resolver: st.GetNamedPropIDs})
	mustNoErr(t, "export the appointment", err)
	for _, want := range []string{"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=3", "TZID=Europe/Berlin", "SUMMARY:Planning", "LOCATION:Room 4", "bob@hermex.test"} {
		if !strings.Contains(strings.ReplaceAll(string(ical), "\r\n ", ""), want) {
			t.Errorf("the CalDAV form lacks %q:\n%s", want, ical)
		}
	}
	events, err := CalendarFreeBusy(st, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), false)
	st.Close()
	mustNoErr(t, "read free/busy", err)
	if len(events) != 3 || events[1].StartTime != "2026-10-12T07:00:00Z" || events[1].BusyType != "Tentative" {
		t.Errorf("free/busy = %+v, want three tentative Mondays at 07:00Z", events)
	}
	if n := mailCount(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("bob's inbox holds %d messages, want the invitation", n)
	}
	if n := mailCount(t, paths["alice@hermex.test"], int64(mapi.PrivateFIDSentItems)); n != 1 {
		t.Errorf("alice's Sent Items holds %d messages, want the invitation copy", n)
	}
}

// TestCreatedCalendarItemKeepsItsHTMLBody proves an HTML body a client writes is
// stored and read back as HTML, not dropped because iCalendar carries only text.
func TestCreatedCalendarItemKeepsItsHTMLBody(t *testing.T) {
	ts, _ := availabilityServer(t)
	_, out := soapPost(t, ts, createCalendarReq("SendToNone",
		`<t:Subject>Styled</t:Subject><t:Body BodyType="HTML">&lt;p&gt;&lt;b&gt;bold&lt;/b&gt;&lt;/p&gt;</t:Body>`+
			`<t:Start>2026-10-05T09:00:00Z</t:Start><t:End>2026-10-05T10:00:00Z</t:End>`), true)
	ids := itemIDRE.FindStringSubmatch(out)
	if len(ids) != 2 {
		t.Fatalf("the create returned no ItemId: %s", out)
	}
	_, got := soapPost(t, ts, getItemReq(ids[1]), true)
	wantContains(t, "the HTML body", got, `<Body BodyType="HTML">&lt;p&gt;&lt;b&gt;bold&lt;/b&gt;&lt;/p&gt;</Body>`)
}

// TestCreateItemCalendarItemRefusals proves a calendar item with no
// SendMeetingInvitations, with a field this server would drop, or ending before it
// starts is refused, and that SendToNone stores the meeting and tells nobody.
func TestCreateItemCalendarItemRefusals(t *testing.T) {
	ts, paths := availabilityServer(t)
	_, out := soapPost(t, ts, createCalendarReq("", weeklyMeeting), true)
	wantContains(t, "no SendMeetingInvitations", out, "ErrorSendMeetingInvitationsRequired")
	_, out = soapPost(t, ts, createCalendarReq("SendToNone", weeklyMeeting+`<t:IsResponseRequested>true</t:IsResponseRequested><t:AppointmentReplyTime>2026-10-01T00:00:00Z</t:AppointmentReplyTime>`), true)
	wantContains(t, "a field that would be dropped", out, "ErrorInvalidPropertySet")
	_, out = soapPost(t, ts, createCalendarReq("SendToNone",
		`<t:Subject>Backwards</t:Subject><t:Start>2026-10-05T09:00:00Z</t:Start><t:End>2026-10-05T08:00:00Z</t:End>`), true)
	wantContains(t, "an end before the start", out, "ErrorCalendarEndDateIsEarlierThanStartDate")

	_, out = soapPost(t, ts, createCalendarReq("SendToNone", weeklyMeeting), true)
	wantContains(t, "SendToNone", out, `ResponseClass="Success"`)
	if n := mailCount(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox)); n != 0 {
		t.Errorf("SendToNone delivered %d messages to bob, want none", n)
	}
}
