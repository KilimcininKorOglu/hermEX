package meeting

import (
	"errors"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// describe reads the view of an Inbox message, with the calendar part it carries.
func describe(t *testing.T, st *objectstore.Store, id int64) View {
	t.Helper()
	ics, _ := inboxCalendarPart(st, id)
	v, ok, err := Describe(st, id, ics)
	if err != nil || !ok {
		t.Fatalf("Describe = ok %v, err %v; want a meeting message", ok, err)
	}
	return v
}

// lastInboxID is the message id of the last message in the Inbox.
func lastInboxID(t *testing.T, st *objectstore.Store) int64 {
	t.Helper()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil || len(msgs) == 0 {
		t.Fatalf("inbox holds %d messages (err %v)", len(msgs), err)
	}
	return msgs[len(msgs)-1].ID
}

// requestInInbox opens a mailbox holding a request for the meeting uid in its Inbox.
func requestInInbox(t *testing.T, uid string) (*objectstore.Store, int64) {
	t.Helper()
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: requestClass},
		{Tag: mapi.PrSubject, Value: "Sync"},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "organizer@hermex.test"},
		{Tag: tags.UID, Value: uid},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return st, reqID
}

// answerAndSee answers the meeting through the message at answered and checks the
// answer the request at reqID then shows. It returns what the response filed.
func answerAndSee(t *testing.T, st *objectstore.Store, answered, reqID int64, response int32) int64 {
	t.Helper()
	calID, err := Respond(st, nil, nil, "alice@hermex.test", answered, response, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := describe(t, st, reqID).Response; got != response {
		t.Errorf("after answering %d through message %d, the request shows %d", response, answered, got)
	}
	return calID
}

// TestDescribeShowsTheAnswerGiven follows a request through the answers an attendee
// gives it. The answer is read from the meeting on the calendar, so one given from
// the calendar item is shown on the request too, and from the request's own stamp
// once a decline took the meeting off the calendar.
func TestDescribeShowsTheAnswerGiven(t *testing.T) {
	st, reqID := requestInInbox(t, "view-1")
	if v := describe(t, st, reqID); v.Kind != KindRequest || v.Response != 0 || !v.ResponseRequested || v.Organizer {
		t.Fatalf("an unanswered request = %+v", v)
	}
	calID := answerAndSee(t, st, reqID, reqID, ResponseAccepted)
	answerAndSee(t, st, calID, reqID, ResponseTentative)
	answerAndSee(t, st, reqID, reqID, ResponseDeclined)
}

// TestDescribeTheOwnInvitation reads the copy of the invitation the organizer keeps:
// it is the mailbox's own meeting, with nothing to answer.
func TestDescribeTheOwnInvitation(t *testing.T) {
	st, tags, _ := organizerWithEvent(t, "view-2", "bob@hermex.test")
	sentID, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: requestClass},
		{Tag: tags.UID, Value: "view-2"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if v := describe(t, st, sentID); !v.Organizer || v.Response != 0 {
		t.Errorf("the own invitation = %+v, want the organizer's with no answer", v)
	}
}

// TestDescribeResponses reads an attendee's answer and a counter proposal as the
// organizer receives them.
func TestDescribeResponses(t *testing.T) {
	st, _, _ := organizerWithEvent(t, "view-3", "bob@hermex.test")

	if v := describe(t, st, deliverReply(t, st, "view-3", "bob@hermex.test", "DECLINED")); v.Kind != KindResponse || v.Response != ResponseDeclined {
		t.Errorf("a declining reply = %+v", v)
	}
	v := describe(t, st, deliverCounter(t, st, "view-3", "bob@hermex.test", ""))
	if v.Kind != KindCounter || v.Response != ResponseTentative || v.Proposal == nil {
		t.Fatalf("a counter proposal = %+v", v)
	}
	if got := mapi.NTTimeToUnix(v.Proposal.Start).UTC(); !got.Equal(time.Date(2026, 7, 1, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("proposed start = %v, want 2026-07-01 16:00 UTC", got)
	}
}

// TestCanceledMeetingIsRemoved removes from the calendar the meeting its organizer
// canceled: the cancellation offers it until it is gone, and then has nothing left
// to remove.
func TestCanceledMeetingIsRemoved(t *testing.T) {
	st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
	deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", singleEvent("0"))
	deliverScheduling(t, accounts, "organizer@hermex.test", "CANCEL", singleEvent("1")+"STATUS:CANCELLED\r\n")
	id := lastInboxID(t, st)
	ics, _ := inboxCalendarPart(st, id)

	if v := describe(t, st, id); v.Kind != KindCancellation || !v.Removable {
		t.Fatalf("the cancellation = %+v, want its meeting removable", v)
	}
	if err := RemoveCanceled(st, id, ics); err != nil {
		t.Fatal(err)
	}
	if objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar)); err != nil || len(objs) != 0 {
		t.Errorf("calendar holds %d items (err %v), want the canceled meeting gone", len(objs), err)
	}
	if v := describe(t, st, id); v.Removable {
		t.Error("the cancellation still offers a meeting that is gone")
	}
	if err := RemoveCanceled(st, id, ics); !errors.Is(err, ErrNotCanceled) {
		t.Errorf("removing again: err = %v, want ErrNotCanceled", err)
	}
}

// TestForgedCancellationRemovesNothing proves a cancellation someone other than the
// organizer sent offers nothing to remove, and removing through it is refused: the
// meeting stays on the calendar.
func TestForgedCancellationRemovesNothing(t *testing.T) {
	st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
	deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", singleEvent("0"))
	deliverScheduling(t, accounts, "mallory@evil.example", "CANCEL", singleEvent("1"))
	id := lastInboxID(t, st)
	ics, _ := inboxCalendarPart(st, id)

	if v := describe(t, st, id); v.Removable {
		t.Error("a forged cancellation offers the meeting for removal")
	}
	if err := RemoveCanceled(st, id, ics); !errors.Is(err, ErrNotCanceled) {
		t.Errorf("err = %v, want ErrNotCanceled", err)
	}
	theMeeting(t, st)
}

// TestCanceledInstanceIsRemoved removes the one instance of a series its organizer
// canceled: the series stays, and the instance is excluded rather than kept as a
// canceled override.
func TestCanceledInstanceIsRemoved(t *testing.T) {
	st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
	deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", dailySeries(false))
	deliverScheduling(t, accounts, "organizer@hermex.test", "CANCEL",
		"RECURRENCE-ID:20260621T100000Z\r\nDTSTART:20260621T100000Z\r\nDTEND:20260621T110000Z\r\nSEQUENCE:1\r\nSTATUS:CANCELLED\r\n")
	id := lastInboxID(t, st)
	ics, _ := inboxCalendarPart(st, id)

	if v := describe(t, st, id); !v.Removable {
		t.Fatal("the canceled instance is not offered for removal")
	}
	if err := RemoveCanceled(st, id, ics); err != nil {
		t.Fatal(err)
	}
	v, _ := theMeeting(t, st).Get(mapi.PrIcalOriginal)
	ical, _ := v.([]byte)
	if !strings.Contains(string(ical), "EXDATE:20260621T100000Z") || strings.Contains(string(ical), "RECURRENCE-ID") {
		t.Errorf("the instance is not excluded from the series:\n%s", ical)
	}
}

// TestRemoveCanceledNeedsACancellation proves only a cancellation removes anything.
func TestRemoveCanceledNeedsACancellation(t *testing.T) {
	st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
	deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", singleEvent("0"))
	id := lastInboxID(t, st)
	ics, _ := inboxCalendarPart(st, id)
	if err := RemoveCanceled(st, id, ics); !errors.Is(err, ErrNotCanceled) {
		t.Errorf("removing through a request: err = %v, want ErrNotCanceled", err)
	}
	theMeeting(t, st)
}
