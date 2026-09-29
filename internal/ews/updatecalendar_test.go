package ews

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// updateCalendarReq is an UpdateItem of one calendar item, named by the given
// item reference element, setting the given SetItemField elements.
func updateCalendarReq(send, ref, fields string) string {
	return wrapRequest(`<UpdateItem xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" ConflictResolution="AutoResolve" SendMeetingInvitationsOrCancellations="` + send + `">` +
		`<ItemChanges><t:ItemChange>` + ref + `<t:Updates>` + fields + `</t:Updates></t:ItemChange></ItemChanges></UpdateItem>`)
}

// setCalendarField is one SetItemField of a calendar item field.
func setCalendarField(uri, value string) string {
	return `<t:SetItemField><t:FieldURI FieldURI="` + uri + `"/><t:CalendarItem>` + value + `</t:CalendarItem></t:SetItemField>`
}

// octoberStarts returns the start of every busy block alice's calendar holds in
// October 2026.
func octoberStarts(t *testing.T, path string) []string {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open alice", err)
	defer st.Close()
	events, err := CalendarFreeBusy(st, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), false)
	mustNoErr(t, "read free/busy", err)
	var out []string
	for _, e := range events {
		out = append(out, e.StartTime)
	}
	return out
}

// exportedAppointment returns the iCalendar CalDAV serves for alice's only
// appointment, unfolded.
func exportedAppointment(t *testing.T, path string) string {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open alice", err)
	defer st.Close()
	msg, err := st.OpenMessage(calendarObjectID(t, st))
	mustNoErr(t, "open the appointment", err)
	ical, err := oxcical.Export(msg, oxcical.Options{Resolver: st.GetNamedPropIDs})
	mustNoErr(t, "export the appointment", err)
	return strings.ReplaceAll(string(ical), "\r\n ", "")
}

// TestUpdateItemEditsACalendarItem proves UpdateItem edits a stored series in
// place: the new subject and location reach CalDAV, a deleted occurrence stays
// deleted while the pattern holds, a new start moves every instance, and the
// attendee receives the updated request. UpdateItem refused every calendar field
// with ErrorInvalidPropertySet before.
func TestUpdateItemEditsACalendarItem(t *testing.T) {
	ts, paths := availabilityServer(t)
	master := createdMeeting(t, ts)
	alice := paths["alice@hermex.test"]
	ref := `<t:ItemId Id="` + master + `"/>`

	_, out := soapPost(t, ts, updateCalendarReq("", ref, setCalendarField("item:Subject", "<t:Subject>Renamed</t:Subject>")), true)
	wantContains(t, "no SendMeetingInvitationsOrCancellations", out, "ErrorSendMeetingInvitationsOrCancellationsRequired")
	_, out = soapPost(t, ts, updateCalendarReq("SendToNone", ref, setCalendarField("calendar:IsResponseRequested", "")), true)
	wantContains(t, "a field it does not write", out, "ErrorInvalidPropertySet")

	_, out = soapPost(t, ts, deleteCalendarReq("SendToNone", `<t:OccurrenceItemId RecurringMasterId="`+master+`" InstanceIndex="2"/>`), true)
	wantContains(t, "the occurrence delete", out, `ResponseClass="Success"`)

	_, out = soapPost(t, ts, updateCalendarReq("SendToAllAndSaveCopy", ref,
		setCalendarField("item:Subject", "<t:Subject>Renamed</t:Subject>")+
			setCalendarField("calendar:Location", "<t:Location>Room 9</t:Location>")), true)
	wantContains(t, "the update", out, `ResponseClass="Success"`)
	ical := exportedAppointment(t, alice)
	for _, want := range []string{"SUMMARY:Renamed", "LOCATION:Room 9", "RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=3", "bob@hermex.test"} {
		if !strings.Contains(ical, want) {
			t.Errorf("the CalDAV form lacks %q:\n%s", want, ical)
		}
	}
	if got := octoberStarts(t, alice); len(got) != 2 {
		t.Errorf("busy blocks after the rename = %v, want the two Mondays left", got)
	}
	if n := mailCount(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox)); n != 2 {
		t.Errorf("bob's inbox holds %d messages, want the invitation and the update", n)
	}

	_, out = soapPost(t, ts, updateCalendarReq("SendToNone", ref,
		setCalendarField("calendar:Start", "<t:Start>2026-10-05T06:00:00Z</t:Start>")+
			setCalendarField("calendar:End", "<t:End>2026-10-05T07:00:00Z</t:End>")), true)
	wantContains(t, "the move", out, `ResponseClass="Success"`)
	got := octoberStarts(t, alice)
	if len(got) != 3 || got[0] != "2026-10-05T06:00:00Z" || got[2] != "2026-10-19T06:00:00Z" {
		t.Errorf("busy blocks after the move = %v, want three Mondays at 06:00Z", got)
	}
}

// TestUpdateItemMovesOneOccurrence proves an occurrence named by its master and
// index moves alone, and that it takes only a new span.
func TestUpdateItemMovesOneOccurrence(t *testing.T) {
	ts, paths := availabilityServer(t)
	master := createdMeeting(t, ts)
	ref := `<t:OccurrenceItemId RecurringMasterId="` + master + `" InstanceIndex="2"/>`

	_, out := soapPost(t, ts, updateCalendarReq("SendToNone", ref, setCalendarField("item:Subject", "<t:Subject>One</t:Subject>")), true)
	wantContains(t, "a subject on an occurrence", out, "ErrorInvalidPropertySet")

	_, out = soapPost(t, ts, updateCalendarReq("SendOnlyToAll", ref,
		setCalendarField("calendar:Start", "<t:Start>2026-10-13T07:00:00Z</t:Start>")+
			setCalendarField("calendar:End", "<t:End>2026-10-13T08:00:00Z</t:End>")), true)
	wantContains(t, "the occurrence move", out, `ResponseClass="Success"`)
	got := octoberStarts(t, paths["alice@hermex.test"])
	want := []string{"2026-10-05T07:00:00Z", "2026-10-13T07:00:00Z", "2026-10-19T07:00:00Z"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("busy blocks = %v, want %v", got, want)
	}
	if n := mailCount(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox)); n != 2 {
		t.Errorf("bob's inbox holds %d messages, want the invitation and the instance update", n)
	}
}
