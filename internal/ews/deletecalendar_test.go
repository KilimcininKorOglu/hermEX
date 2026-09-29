package ews

import (
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// deleteCalendarReq is a DeleteItem of one entry of an ItemIds list with the given
// SendMeetingCancellations value ("" leaves the attribute out).
func deleteCalendarReq(cancellations, ref string) string {
	attr := ""
	if cancellations != "" {
		attr = ` SendMeetingCancellations="` + cancellations + `"`
	}
	return wrapRequest(`<DeleteItem xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" DeleteType="MoveToDeletedItems"` + attr + `>` +
		`<ItemIds>` + ref + `</ItemIds></DeleteItem>`)
}

// createdMeeting creates weeklyMeeting with its invitation sent and returns its
// item id.
func createdMeeting(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, out := soapPost(t, ts, createCalendarReq("SendToAllAndSaveCopy", weeklyMeeting), true)
	ids := itemIDRE.FindStringSubmatch(out)
	if len(ids) != 2 {
		t.Fatalf("the create returned no ItemId: %s", out)
	}
	return ids[1]
}

// octoberBusy returns the busy blocks alice's calendar holds in October 2026.
func octoberBusy(t *testing.T, path string) int {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open alice", err)
	defer st.Close()
	events, err := CalendarFreeBusy(st, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), false)
	mustNoErr(t, "read free/busy", err)
	return len(events)
}

// TestDeleteItemDeletesACalendarItem proves DeleteItem removes a meeting the
// caller organizes and sends its attendee the cancellation. DeleteItem addressed
// every item by its IMAP uid, which an appointment does not have, so deleting one
// failed with ErrorItemNotFound and the meeting stayed.
func TestDeleteItemDeletesACalendarItem(t *testing.T) {
	ts, paths := availabilityServer(t)
	master := createdMeeting(t, ts)

	_, out := soapPost(t, ts, deleteCalendarReq("", `<t:ItemId Id="`+master+`"/>`), true)
	wantContains(t, "no SendMeetingCancellations", out, "ErrorSendMeetingCancellationsRequired")

	_, out = soapPost(t, ts, deleteCalendarReq("SendToAllAndSaveCopy", `<t:ItemId Id="`+master+`"/>`), true)
	wantContains(t, "the delete", out, `ResponseClass="Success"`)
	if n := octoberBusy(t, paths["alice@hermex.test"]); n != 0 {
		t.Errorf("alice's calendar still holds %d busy blocks, want none", n)
	}
	if n := mailCount(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox)); n != 2 {
		t.Errorf("bob's inbox holds %d messages, want the invitation and the cancellation", n)
	}
	if n := mailCount(t, paths["alice@hermex.test"], int64(mapi.PrivateFIDSentItems)); n != 2 {
		t.Errorf("alice's Sent Items holds %d messages, want both copies", n)
	}
}

// TestDeleteItemDeletesOneOccurrence proves deleting an occurrence, named by its
// master and index, removes that one instance and tells the attendee, while the
// other instances stay.
func TestDeleteItemDeletesOneOccurrence(t *testing.T) {
	ts, paths := availabilityServer(t)
	master := createdMeeting(t, ts)

	_, out := soapPost(t, ts, deleteCalendarReq("SendOnlyToAll", `<t:OccurrenceItemId RecurringMasterId="`+master+`" InstanceIndex="2"/>`), true)
	wantContains(t, "the delete", out, `ResponseClass="Success"`)
	if n := octoberBusy(t, paths["alice@hermex.test"]); n != 2 {
		t.Errorf("alice's calendar holds %d busy blocks, want the two other Mondays", n)
	}
	if n := mailCount(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox)); n != 2 {
		t.Errorf("bob's inbox holds %d messages, want the invitation and the cancellation", n)
	}
	if n := mailCount(t, paths["alice@hermex.test"], int64(mapi.PrivateFIDSentItems)); n != 1 {
		t.Errorf("alice's Sent Items holds %d messages, want only the invitation copy", n)
	}

	// A deleted occurrence keeps its index, so the index names it still and the
	// next one keeps its own.
	_, out = soapPost(t, ts, deleteCalendarReq("SendToNone", `<t:OccurrenceItemId RecurringMasterId="`+master+`" InstanceIndex="2"/>`), true)
	wantContains(t, "the deleted occurrence", out, "ErrorCalendarOccurrenceIsDeletedFromRecurrence")
	_, out = soapPost(t, ts, deleteCalendarReq("SendToNone", `<t:OccurrenceItemId RecurringMasterId="`+master+`" InstanceIndex="3"/>`), true)
	wantContains(t, "the second delete", out, `ResponseClass="Success"`)
	if n := octoberBusy(t, paths["alice@hermex.test"]); n != 1 {
		t.Errorf("alice's calendar holds %d busy blocks, want one Monday", n)
	}
	if n := mailCount(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox)); n != 2 {
		t.Errorf("SendToNone delivered to bob: %d messages", n)
	}
}
