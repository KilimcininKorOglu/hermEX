package ews

import (
	"net/http"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// toMAPISeries removes the verbatim iCalendar of alice's only appointment, which
// leaves the series in the form a MAPI client writes: its recurrence blob alone.
func toMAPISeries(t *testing.T, path string) {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open alice", err)
	defer mustCloseStore(t, st)
	mustNoErr(t, "drop the iCalendar", st.ModifyMessageProperties(calendarObjectID(t, st), nil, mapi.PrIcalOriginal))
}

// keepsNoICalendar reports whether alice's appointment still has no verbatim
// iCalendar, the form a MAPI client reads its series from.
func keepsNoICalendar(t *testing.T, path string) bool {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open alice", err)
	defer mustCloseStore(t, st)
	props, err := st.GetMessageProperties(calendarObjectID(t, st), mapi.PrIcalOriginal)
	mustNoErr(t, "read the appointment", err)
	return !props.Has(mapi.PrIcalOriginal)
}

// TestOccurrenceEditsReachAMAPISeries proves one occurrence of a series a MAPI
// client wrote, which keeps no iCalendar, is deleted and moved through its
// recurrence blob while the others stay and the series keeps its MAPI form. Both
// were refused with ErrorItemSave before.
func TestOccurrenceEditsReachAMAPISeries(t *testing.T) {
	ts, paths := availabilityServer(t)
	master := createdMeeting(t, ts)
	alice := paths["alice@hermex.test"]
	toMAPISeries(t, alice)

	resp, out := soapPost(t, ts, deleteCalendarReq("SendToNone", `<t:OccurrenceItemId RecurringMasterId="`+master+`" InstanceIndex="2"/>`), true)
	wantEq(t, "occurrence delete status", resp.StatusCode, http.StatusOK)
	wantContains(t, "the occurrence delete", out, `ResponseClass="Success"`)
	resp, out = soapPost(t, ts, updateCalendarReq("SendToNone", `<t:OccurrenceItemId RecurringMasterId="`+master+`" InstanceIndex="3"/>`,
		setCalendarField("calendar:Start", "<t:Start>2026-10-20T07:00:00Z</t:Start>")+
			setCalendarField("calendar:End", "<t:End>2026-10-20T08:00:00Z</t:End>")), true)
	wantEq(t, "occurrence move status", resp.StatusCode, http.StatusOK)
	wantContains(t, "the occurrence move", out, `ResponseClass="Success"`)

	got := strings.Join(octoberStarts(t, alice), ",")
	if want := "2026-10-05T07:00:00Z,2026-10-20T07:00:00Z"; got != want {
		t.Errorf("busy blocks = %s, want %s", got, want)
	}
	if !keepsNoICalendar(t, alice) {
		t.Error("the edit gave the MAPI series an iCalendar body")
	}
	resp, out = soapPost(t, ts, deleteCalendarReq("SendToNone", `<t:OccurrenceItemId RecurringMasterId="`+master+`" InstanceIndex="2"/>`), true)
	wantEq(t, "deleted occurrence status", resp.StatusCode, http.StatusOK)
	wantContains(t, "the deleted occurrence", out, "ErrorCalendarOccurrenceIsDeletedFromRecurrence")
	if gotAfter := strings.Join(octoberStarts(t, alice), ","); gotAfter != "2026-10-05T07:00:00Z,2026-10-20T07:00:00Z" {
		t.Errorf("busy blocks changed after deleted-occurrence refusal: %s", gotAfter)
	}
}
