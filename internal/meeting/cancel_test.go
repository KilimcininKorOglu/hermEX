package meeting

import (
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

const cancelUID = "cancel-1@hermex.test"

// cancelHarness is an auto-accepting room with the delivery hooks installed, so a
// scheduling message delivered to it is processed the way every daemon processes
// one.
func cancelHarness(t *testing.T, cfg objectstore.MeetingConfig) (*objectstore.Store, directory.Accounts) {
	t.Helper()
	cfg.AutoAccept = true
	st, _, accounts := apSetup(t, cfg)
	request, reply, cancel := mta.OnMeetingRequest, mta.OnMeetingReply, mta.OnMeetingCancel
	t.Cleanup(func() { mta.OnMeetingRequest, mta.OnMeetingReply, mta.OnMeetingCancel = request, reply, cancel })
	InstallDeliveryHooks(logging.New(&hookSink{}))
	return st, accounts
}

// deliverScheduling delivers one iTIP message from sender to the room.
func deliverScheduling(t *testing.T, accounts directory.Accounts, sender, method, vevent string) {
	t.Helper()
	raw := "From: " + sender + "\r\nTo: room@hermex.test\r\nSubject: " + method + "\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/calendar; method=" + method + "; charset=UTF-8\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//test//EN\r\nMETHOD:" + method + "\r\n" +
		"BEGIN:VEVENT\r\nUID:" + cancelUID + "\r\nDTSTAMP:20260619T090000Z\r\nSUMMARY:Sync\r\n" +
		"ORGANIZER:mailto:organizer@hermex.test\r\nATTENDEE:mailto:room@hermex.test\r\n" + vevent +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	if _, err := mta.DeliverAndRelay(accounts, nil, sender, []string{"room@hermex.test"}, []byte(raw), apBase); err != nil {
		t.Fatal(err)
	}
}

// singleEvent is the one-hour meeting at revision seq.
func singleEvent(seq string) string {
	return "DTSTART:20260620T100000Z\r\nDTEND:20260620T110000Z\r\nSEQUENCE:" + seq + "\r\n"
}

// dailySeries is the three-day series; the 21 June instance is excluded when
// gap is set.
func dailySeries(gap bool) string {
	body := "DTSTART:20260620T100000Z\r\nDTEND:20260620T110000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\n"
	if gap {
		body += "EXDATE:20260621T100000Z\r\n"
	}
	return body
}

// theMeeting reads the room's one calendar item.
func theMeeting(t *testing.T, st *objectstore.Store) mapi.PropertyValues {
	t.Helper()
	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil || len(objs) != 1 {
		t.Fatalf("calendar holds %d items (err %v), want the one meeting", len(objs), err)
	}
	pv, err := st.GetMessageProperties(objs[0].ID, tags.State, tags.Busy, mapi.PrIcalOriginal)
	if err != nil {
		t.Fatal(err)
	}
	return pv
}

// cancelled reports whether the meeting is marked cancelled, and its free/busy.
func cancelled(t *testing.T, st *objectstore.Store) (bool, int32) {
	t.Helper()
	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	pv := theMeeting(t, st)
	return longVal(pv, tags.State)&asfCanceled != 0, longVal(pv, tags.Busy)
}

// lastInbox reads the last message delivered to the room's Inbox.
func lastInbox(t *testing.T, st *objectstore.Store, tags ...mapi.PropTag) mapi.PropertyValues {
	t.Helper()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil || len(msgs) == 0 {
		t.Fatalf("inbox holds %d messages (err %v)", len(msgs), err)
	}
	pv, err := st.GetMessageProperties(msgs[len(msgs)-1].ID, tags...)
	if err != nil {
		t.Fatal(err)
	}
	return pv
}

// TestCancellationMarksTheMeeting delivers the organizer's cancellation of a
// meeting the room accepted. A cancellation used to change nothing until someone
// declined it by hand; now the meeting stays, marked cancelled and free.
func TestCancellationMarksTheMeeting(t *testing.T) {
	st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
	deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", singleEvent("0"))
	deliverScheduling(t, accounts, "organizer@hermex.test", "CANCEL", singleEvent("1")+"STATUS:CANCELLED\r\n")

	if isCancelled, busy := cancelled(t, st); !isCancelled || busy != busyFree {
		t.Errorf("meeting cancelled = %v, busy = %d; want cancelled and free", isCancelled, busy)
	}
	if !boolVal(lastInbox(t, st, mapi.PrProcessed), mapi.PrProcessed) {
		t.Error("the cancellation was not marked processed, so it would be applied again")
	}
}

// TestCancellationFromAnotherSenderIsRefused delivers a cancellation someone other
// than the organizer sent. The UID alone would let any attendee cancel the meeting
// in another attendee's calendar.
func TestCancellationFromAnotherSenderIsRefused(t *testing.T) {
	st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
	deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", singleEvent("0"))
	deliverScheduling(t, accounts, "mallory@evil.example", "CANCEL", singleEvent("1"))

	if isCancelled, busy := cancelled(t, st); isCancelled || busy != busyBusy {
		t.Errorf("a forged cancellation changed the meeting: cancelled = %v, busy = %d", isCancelled, busy)
	}
}

// TestOutOfDateCancellationChangesNothing delivers a cancellation older than the
// meeting the room holds: it is marked out of date and the meeting stands.
func TestOutOfDateCancellationChangesNothing(t *testing.T) {
	st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
	deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", singleEvent("2"))
	deliverScheduling(t, accounts, "organizer@hermex.test", "CANCEL", singleEvent("1"))

	if isCancelled, _ := cancelled(t, st); isCancelled {
		t.Error("an out-of-date cancellation cancelled the meeting")
	}
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameMeetingType})
	if err != nil || ids[0] == 0 {
		t.Fatalf("the meeting type was never written (err %v)", err)
	}
	tag := mapi.MakeTag(ids[0], mapi.PtLong)
	if got := longVal(lastInbox(t, st, tag), tag); got != mtgOutOfDate {
		t.Errorf("cancellation meeting type = %#x, want out of date", got)
	}
}

// TestCancellationLeftWhenTurnedOff proves a mailbox that turned processing off
// keeps the meeting as it was and leaves the cancellation unprocessed.
func TestCancellationLeftWhenTurnedOff(t *testing.T) {
	st, accounts := cancelHarness(t, objectstore.MeetingConfig{LeaveCancellationsUnprocessed: true})
	deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", singleEvent("0"))
	deliverScheduling(t, accounts, "organizer@hermex.test", "CANCEL", singleEvent("1"))

	if isCancelled, _ := cancelled(t, st); isCancelled {
		t.Error("the meeting was cancelled although the mailbox turned processing off")
	}
	if boolVal(lastInbox(t, st, mapi.PrProcessed), mapi.PrProcessed) {
		t.Error("the cancellation was marked processed although nothing applied it")
	}
}

// TestInstanceCancellationKeepsTheSeries delivers a cancellation for the second
// instance of a series: only that instance is marked cancelled.
func TestInstanceCancellationKeepsTheSeries(t *testing.T) {
	st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
	deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", dailySeries(false))
	deliverScheduling(t, accounts, "organizer@hermex.test", "CANCEL",
		"RECURRENCE-ID:20260621T100000Z\r\nDTSTART:20260621T100000Z\r\nDTEND:20260621T110000Z\r\nSEQUENCE:1\r\nSTATUS:CANCELLED\r\n")

	if isCancelled, _ := cancelled(t, st); isCancelled {
		t.Error("an instance cancellation cancelled the whole series")
	}
	v, _ := theMeeting(t, st).Get(mapi.PrIcalOriginal)
	ical, _ := v.([]byte)
	for _, want := range []string{"RRULE:FREQ=DAILY;COUNT=3", "RECURRENCE-ID:20260621T100000Z", "STATUS:CANCELLED"} {
		if !strings.Contains(string(ical), want) {
			t.Errorf("series lacks %q:\n%s", want, ical)
		}
	}
}

// TestCancelledInstanceIsNotRecreated delivers a cancellation for an instance the
// series already excludes. MS-OXOCAL forbids recreating a deleted exception.
func TestCancelledInstanceIsNotRecreated(t *testing.T) {
	st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
	deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", dailySeries(true))
	deliverScheduling(t, accounts, "organizer@hermex.test", "CANCEL",
		"RECURRENCE-ID:20260621T100000Z\r\nDTSTART:20260621T100000Z\r\nDTEND:20260621T110000Z\r\nSEQUENCE:1\r\n")

	v, _ := theMeeting(t, st).Get(mapi.PrIcalOriginal)
	if ical, _ := v.([]byte); strings.Contains(string(ical), "RECURRENCE-ID") {
		t.Errorf("an excluded instance came back as an override:\n%s", ical)
	}
}
