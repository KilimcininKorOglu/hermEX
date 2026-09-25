package meeting

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// deliverCounter appends an iTIP COUNTER from attendee proposing 16:00 to 17:00 on
// 2026-07-01 UTC, with the PARTSTAT parameter when partstat is not empty.
func deliverCounter(t *testing.T, st *objectstore.Store, uid, attendee, partstat string) int64 {
	t.Helper()
	param := ""
	if partstat != "" {
		param = ";PARTSTAT=" + partstat
	}
	raw := "From: " + attendee + "\r\nTo: organizer@hermex.test\r\nSubject: New Time Proposed\r\n" +
		"Content-Type: text/calendar; method=COUNTER\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nMETHOD:COUNTER\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\n" +
		"DTSTART:20260701T160000Z\r\nDTEND:20260701T170000Z\r\n" +
		"ATTENDEE" + param + ":mailto:" + attendee + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1718200000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// proposalState is what the organizer's meeting records about the attendee's
// proposal: the row's flag and proposed start, and the meeting's flag and count.
type proposalState struct {
	rowProposed   bool
	rowStart      uint64
	eventFlag     bool
	eventProposed int32
}

func readProposalState(t *testing.T, st *objectstore.Store, recipID int64) proposalState {
	t.Helper()
	row, err := st.GetRecipientProperties(recipID, mapi.PrRecipientProposed, mapi.PrRecipientProposedStartTime)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := resolveCounterTags(st)
	if err != nil {
		t.Fatal(err)
	}
	eventID := eventOf(t, st)
	ev, err := st.GetMessageProperties(eventID, ct.flag, ct.number)
	if err != nil {
		t.Fatal(err)
	}
	var s proposalState
	s.rowProposed, _ = mustGet(row, mapi.PrRecipientProposed).(bool)
	s.rowStart, _ = mustGet(row, mapi.PrRecipientProposedStartTime).(uint64)
	s.eventFlag, _ = mustGet(ev, ct.flag).(bool)
	s.eventProposed, _ = mustGet(ev, ct.number).(int32)
	return s
}

func mustGet(p mapi.PropertyValues, tag mapi.PropTag) any {
	v, _ := p.Get(tag)
	return v
}

// mustProcess runs the organizer-side pass on a response bob sent.
func mustProcess(t *testing.T, st *objectstore.Store, msgID int64) {
	t.Helper()
	if handled, err := ProcessReply(st, "bob@hermex.test", msgID); !handled || err != nil {
		t.Fatalf("response handled=%v err=%v", handled, err)
	}
}

// eventOf returns the organizer's one calendar event.
func eventOf(t *testing.T, st *objectstore.Store) int64 {
	t.Helper()
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil || len(objs) != 1 {
		t.Fatalf("calendar = %v (%v), want one event", objs, err)
	}
	return objs[0].ID
}

// TestCounterIsTrackedOnTheOrganizersMeeting follows [MS-OXOCAL] 3.1.4.8.5.3: a
// proposal marks the attendee and the meeting and counts once per attendee, and a
// later plain response withdraws it. Before, a COUNTER was not processed at all,
// so the organizer's client never saw that a new time had been proposed.
func TestCounterIsTrackedOnTheOrganizersMeeting(t *testing.T) {
	st, tags, recipID := organizerWithEvent(t, "counter-1", "bob@hermex.test")
	proposed := mapi.UnixToNTTime(time.Date(2026, 7, 1, 16, 0, 0, 0, time.UTC))

	for range 2 {
		mustProcess(t, st, deliverCounter(t, st, "counter-1", "bob@hermex.test", "TENTATIVE"))
	}
	want := proposalState{rowProposed: true, rowStart: proposed, eventFlag: true, eventProposed: 1}
	if got := readProposalState(t, st, recipID); got != want {
		t.Errorf("after two proposals = %+v, want %+v (one attendee counts once)", got, want)
	}
	if got := responseOf(t, st, tags, recipID); got != ResponseTentative {
		t.Errorf("tracking status = %d, want tentative", got)
	}

	mustProcess(t, st, deliverReply(t, st, "counter-1", "bob@hermex.test", "ACCEPTED"))
	got := readProposalState(t, st, recipID)
	if got.rowProposed || got.eventFlag || got.eventProposed != 0 {
		t.Errorf("after accepting = %+v, want the proposal withdrawn", got)
	}
}

// TestCounterWithoutPartstatIsTentative reads a COUNTER that names no PARTSTAT as
// the tentative response it is.
func TestCounterWithoutPartstatIsTentative(t *testing.T) {
	st, tags, recipID := organizerWithEvent(t, "counter-2", "bob@hermex.test")
	if handled, _ := ProcessReply(st, "bob@hermex.test", deliverCounter(t, st, "counter-2", "bob@hermex.test", "")); !handled {
		t.Fatal("a COUNTER without PARTSTAT was not processed")
	}
	if got := responseOf(t, st, tags, recipID); got != ResponseTentative {
		t.Errorf("tracking status = %d, want tentative", got)
	}
	if !readProposalState(t, st, recipID).rowProposed {
		t.Error("the proposal was not recorded")
	}
}

// TestCounterFromAnotherSenderIsIgnored keeps the REPLY rule for a COUNTER: only
// the attendee itself may propose on its row.
func TestCounterFromAnotherSenderIsIgnored(t *testing.T) {
	st, _, recipID := organizerWithEvent(t, "counter-3", "bob@hermex.test")
	msgID := deliverCounter(t, st, "counter-3", "bob@hermex.test", "TENTATIVE")
	if handled, _ := ProcessReply(st, "mallory@hermex.test", msgID); handled {
		t.Error("a COUNTER from another sender was processed")
	}
	if got := readProposalState(t, st, recipID); got != (proposalState{}) {
		t.Errorf("state = %+v, want untouched", got)
	}
}
