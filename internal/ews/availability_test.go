package ews

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// availabilityServer builds an EWS server over alice (the soapPost requester) and
// bob, returning their mailbox paths so a test can seed calendars and permissions.
func availabilityServer(t *testing.T) (*httptest.Server, map[string]string) {
	t.Helper()
	paths := map[string]string{
		"alice@hermex.test": t.TempDir(),
		"bob@hermex.test":   t.TempDir(),
	}
	accs := directory.StaticAccounts{
		"alice@hermex.test": {Password: testPass, MailboxPath: paths["alice@hermex.test"]},
		"bob@hermex.test":   {Password: testPass, MailboxPath: paths["bob@hermex.test"]},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, paths
}

// seedAppointment writes one calendar appointment with the given window, busy
// status, subject, and recurrence flag into the mailbox at path.
func seedAppointment(t *testing.T, path string, start, end time.Time, busyStatus int32, subject string, recurring bool) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole,
		mapi.NameAppointmentEndWhole,
		mapi.NameBusyStatus,
		mapi.NameRecurring,
	})
	if err != nil {
		t.Fatal(err)
	}
	props := mapi.PropertyValues{
		{Tag: namedTag(ids[0], mapi.PtSysTime), Value: mapi.UnixToNTTime(start)},
		{Tag: namedTag(ids[1], mapi.PtSysTime), Value: mapi.UnixToNTTime(end)},
		{Tag: namedTag(ids[2], mapi.PtLong), Value: busyStatus},
		{Tag: mapi.PrSubject, Value: subject},
	}
	if recurring {
		props = append(props, mapi.TaggedPropVal{Tag: namedTag(ids[3], mapi.PtBoolean), Value: true})
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: props}); err != nil {
		t.Fatal(err)
	}
}

// availabilityReq builds a GetUserAvailability SOAP request for one target over a
// time window. When withTZ is false the TimeZone element is omitted.
func availabilityReq(target, start, end string, withTZ bool) string {
	tz := ""
	if withTZ {
		tz = `<t:TimeZone><t:Bias>0</t:Bias></t:TimeZone>`
	}
	return wrapRequest(`<GetUserAvailabilityRequest xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		tz +
		`<t:MailboxDataArray><t:MailboxData><t:Email><t:Address>` + target + `</t:Address></t:Email>` +
		`<t:AttendeeType>Required</t:AttendeeType></t:MailboxData></t:MailboxDataArray>` +
		`<t:FreeBusyViewOptions><t:TimeWindow>` +
		`<t:StartTime>` + start + `</t:StartTime><t:EndTime>` + end + `</t:EndTime>` +
		`</t:TimeWindow><t:RequestedView>Detailed</t:RequestedView></t:FreeBusyViewOptions>` +
		`</GetUserAvailabilityRequest>`)
}

const (
	winStart = "2026-06-19T10:00:00Z"
	winEnd   = "2026-06-19T11:00:00Z"
)

// TestAvailabilityOwnerDetailed confirms a mailbox owner querying their own
// calendar always gets the detailed view, with appointment subject — the implicit
// owner access the seeded free/busy-simple default would otherwise withhold.
func TestAvailabilityOwnerDetailed(t *testing.T) {
	ts, paths := availabilityServer(t)
	start := time.Date(2026, 6, 19, 10, 15, 0, 0, time.UTC)
	end := time.Date(2026, 6, 19, 10, 45, 0, 0, time.UTC)
	seedAppointment(t, paths["alice@hermex.test"], start, end, 2, "Standup", false)

	_, out := soapPost(t, ts, availabilityReq("alice@hermex.test", winStart, winEnd, true), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("not success: %s", out)
	}
	if !strings.Contains(out, ">Detailed<") {
		t.Errorf("owner self-query must yield Detailed view: %s", out)
	}
	if !strings.Contains(out, ">Busy</BusyType>") {
		t.Errorf("expected BusyType Busy: %s", out)
	}
	if !strings.Contains(out, "CalendarEventDetails") || !strings.Contains(out, ">Standup<") {
		t.Errorf("detailed view must carry the subject: %s", out)
	}
}

// TestAvailabilityCrossUserFreeBusy confirms a caller with only the seeded
// free/busy-simple default sees busy blocks but no appointment detail.
func TestAvailabilityCrossUserFreeBusy(t *testing.T) {
	ts, paths := availabilityServer(t)
	start := time.Date(2026, 6, 19, 10, 15, 0, 0, time.UTC)
	end := time.Date(2026, 6, 19, 10, 45, 0, 0, time.UTC)
	seedAppointment(t, paths["bob@hermex.test"], start, end, 2, "Private meeting", false)

	_, out := soapPost(t, ts, availabilityReq("bob@hermex.test", winStart, winEnd, true), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("not success: %s", out)
	}
	if !strings.Contains(out, ">FreeBusy<") {
		t.Errorf("cross-user with free/busy-simple must yield FreeBusy view: %s", out)
	}
	if !strings.Contains(out, ">Busy</BusyType>") {
		t.Errorf("expected the busy block: %s", out)
	}
	if strings.Contains(out, "CalendarEventDetails") || strings.Contains(out, "Private meeting") {
		t.Errorf("free/busy-simple must not leak appointment detail: %s", out)
	}
}

// TestAvailabilityDenied confirms a calendar whose default grant carries no
// free/busy right denies the caller per-mailbox (not an empty all-free view).
func TestAvailabilityDenied(t *testing.T) {
	ts, paths := availabilityServer(t)
	bob, err := objectstore.Open(paths["bob@hermex.test"])
	if err != nil {
		t.Fatal(err)
	}
	// Strip free/busy from bob's calendar default (leave only Visible).
	if err := bob.ModifyPermissions(int64(mapi.PrivateFIDCalendar), false, []objectstore.PermissionChange{
		{Op: objectstore.PermModify, MemberID: mapi.MemberIDDefault, Rights: mapi.FrightsVisible},
	}); err != nil {
		t.Fatal(err)
	}
	bob.Close()

	_, out := soapPost(t, ts, availabilityReq("bob@hermex.test", winStart, winEnd, true), true)
	if !strings.Contains(out, `ResponseClass="Error"`) || !strings.Contains(out, "ErrorFreeBusyGenerationFailed") {
		t.Errorf("expected per-mailbox ErrorFreeBusyGenerationFailed: %s", out)
	}
	if strings.Contains(out, "FreeBusyView") {
		t.Errorf("denied response must not carry a FreeBusyView: %s", out)
	}
}

// TestAvailabilitySpanningEvent confirms an appointment that starts before the
// window and ends after it still appears — the overlap predicate, not containment.
func TestAvailabilitySpanningEvent(t *testing.T) {
	ts, paths := availabilityServer(t)
	start := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC) // before the window
	end := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)  // after the window
	seedAppointment(t, paths["alice@hermex.test"], start, end, 2, "All morning", false)

	_, out := soapPost(t, ts, availabilityReq("alice@hermex.test", winStart, winEnd, true), true)
	if !strings.Contains(out, ">Busy</BusyType>") || !strings.Contains(out, ">All morning<") {
		t.Errorf("a window-spanning appointment must be reported: %s", out)
	}
}

// TestAvailabilityRecurringSkipped confirms a recurring series master is not
// emitted (its stored start/end is only the first instance; expansion is a v1 gap).
func TestAvailabilityRecurringSkipped(t *testing.T) {
	ts, paths := availabilityServer(t)
	start := time.Date(2026, 6, 19, 10, 15, 0, 0, time.UTC)
	end := time.Date(2026, 6, 19, 10, 45, 0, 0, time.UTC)
	seedAppointment(t, paths["alice@hermex.test"], start, end, 2, "Weekly sync", true)

	_, out := soapPost(t, ts, availabilityReq("alice@hermex.test", winStart, winEnd, true), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("not success: %s", out)
	}
	if strings.Contains(out, "CalendarEvent>") || strings.Contains(out, "Weekly sync") {
		t.Errorf("recurring master must be skipped: %s", out)
	}
}

// TestAvailabilityEmptyIsDetailed confirms an empty calendar yields a "Detailed"
// view even for a non-detailed (cross-user free/busy-simple) caller — the
// all-of-over-empty quirk the reference preserves.
func TestAvailabilityEmptyIsDetailed(t *testing.T) {
	ts, paths := availabilityServer(t)
	// Touch bob's store so the folder tree (and the free/busy-simple default) exists,
	// but seed no appointment.
	if st, err := objectstore.Open(paths["bob@hermex.test"]); err != nil {
		t.Fatal(err)
	} else {
		st.Close()
	}

	_, out := soapPost(t, ts, availabilityReq("bob@hermex.test", winStart, winEnd, true), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("not success: %s", out)
	}
	if !strings.Contains(out, ">Detailed<") {
		t.Errorf("empty calendar must yield Detailed view type: %s", out)
	}
}

// TestAvailabilityNoTimeZone confirms a request without a TimeZone reports
// ErrorTimeZone per mailbox rather than faulting the whole request.
func TestAvailabilityNoTimeZone(t *testing.T) {
	ts, _ := availabilityServer(t)
	_, out := soapPost(t, ts, availabilityReq("bob@hermex.test", winStart, winEnd, false), true)
	if !strings.Contains(out, "ErrorTimeZone") {
		t.Errorf("missing TimeZone must yield ErrorTimeZone: %s", out)
	}
}

// TestAvailabilityUnknownUser confirms an unresolvable target reports
// ErrorMailRecipientNotFound for that mailbox.
func TestAvailabilityUnknownUser(t *testing.T) {
	ts, _ := availabilityServer(t)
	_, out := soapPost(t, ts, availabilityReq("nobody@hermex.test", winStart, winEnd, true), true)
	if !strings.Contains(out, "ErrorMailRecipientNotFound") {
		t.Errorf("unknown target must yield ErrorMailRecipientNotFound: %s", out)
	}
}
