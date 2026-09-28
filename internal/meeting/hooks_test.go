package meeting

import (
	"errors"
	"sync"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
)

// hookSink records every event the hook reports.
type hookSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (s *hookSink) Write(e logging.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

// TestMeetingHookRecordsFailure is the swallowed-failure defect. The pass runs
// after delivery has already succeeded, so its error is discarded on purpose; it
// used to go to stderr instead of the central sink, which is where the sibling
// post-delivery passes report and the only place an operator looks. Without it a
// failure leaves the organizer never told the request was accepted or declined and
// nothing explaining why.
func TestMeetingHookRecordsFailure(t *testing.T) {
	sink := &hookSink{}
	failing := func(*objectstore.Store, directory.Accounts, *relay.Spool, string, int64) (bool, error) {
		return false, errors.New("directory unavailable")
	}

	hook := requestHook(failing, logging.New(sink))
	if handled := hook(nil, nil, "room@hermex.test", 42); handled {
		t.Errorf("a failed pass reported the request as handled")
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, e := range sink.events {
		if e.Name == "meeting.autoprocess.fail" {
			if e.User != "room@hermex.test" {
				t.Errorf("event user = %q, want the recipient", e.User)
			}
			if e.Err == "" {
				t.Error("the event carries no error text, so the cause is still unknown")
			}
			return
		}
	}
	t.Errorf("the auto-process failure never reached the central sink; events = %+v", sink.events)
}

// useDeliveryHooks installs the delivery hooks for one test and puts back the ones
// installed before it, all three, so no later test delivers with them.
func useDeliveryHooks(t *testing.T) {
	t.Helper()
	request, reply, cancel := mta.OnMeetingRequest, mta.OnMeetingReply, mta.OnMeetingCancel
	t.Cleanup(func() { mta.OnMeetingRequest, mta.OnMeetingReply, mta.OnMeetingCancel = request, reply, cancel })
	InstallDeliveryHooks(logging.New(&hookSink{}))
}

// deliverRoomInvite delivers the organizer's invitation for uid to the room.
func deliverRoomInvite(t *testing.T, accounts directory.Accounts, uid string) {
	t.Helper()
	invite := []byte("From: organizer@hermex.test\r\nTo: room@hermex.test\r\nSubject: Sync\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/calendar; method=REQUEST; charset=UTF-8\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//test//EN\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20260619T090000Z\r\n" +
		"DTSTART:20260620T100000Z\r\nDTEND:20260620T110000Z\r\nSUMMARY:Sync\r\n" +
		"ORGANIZER:mailto:organizer@hermex.test\r\nATTENDEE:mailto:room@hermex.test\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n")
	if _, err := mta.DeliverAndRelay(accounts, nil, "organizer@hermex.test", []string{"room@hermex.test"}, invite, apBase); err != nil {
		t.Fatal(err)
	}
}

// TestInstalledHooksProcessALocalInvite delivers an invitation the way a webmail,
// EWS, ActiveSync, DAV or MAPI send reaches a local mailbox, through
// mta.DeliverAndRelay in that daemon's own process. Only cmd/mta used to install
// the hooks, so an auto-accepting mailbox left such an invitation unanswered.
func TestInstalledHooksProcessALocalInvite(t *testing.T) {
	st, tags, accounts := apSetup(t, objectstore.MeetingConfig{AutoAccept: true})
	useDeliveryHooks(t)
	deliverRoomInvite(t, accounts, "local-invite-1")
	if got := calBusyStatuses(t, st, tags); len(got) != 1 || got[0] != busyBusy {
		t.Errorf("calendar busy statuses = %v, want the invitation accepted", got)
	}
}

// TestAutomaticResponseNamesTheResponder follows a room's automatic acceptance to
// the organizer. The response is a new message, so nothing of the request's identity
// may stand in for the room's: the room is its sender, and its REPLY names the room as
// the one ATTENDEE with the room's PARTSTAT (RFC 5546 §3.2.3). A REPLY that names no
// attendee is filed at the organizer as another copy of the meeting, and the
// acceptance is never recorded.
func TestAutomaticResponseNamesTheResponder(t *testing.T) {
	room, _, _ := apSetup(t, objectstore.MeetingConfig{AutoAccept: true})
	organizer, tags, recipID := organizerWithEvent(t, "booking-1", "room@hermex.test")
	accounts := directory.StaticAccounts{
		"room@hermex.test":      {MailboxPath: room.Dir()},
		"organizer@hermex.test": {MailboxPath: organizer.Dir()},
	}
	useDeliveryHooks(t)
	deliverRoomInvite(t, accounts, "booking-1")

	msgs, err := organizer.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil || len(msgs) != 1 {
		t.Fatalf("organizer inbox holds %d messages (err %v), want the room's response", len(msgs), err)
	}
	answer, err := organizer.OpenMessage(msgs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for tag, want := range map[mapi.PropTag]string{
		mapi.PrMessageClass:                "IPM.Schedule.Meeting.Resp.Pos",
		mapi.PrSentRepresentingSmtpAddress: "room@hermex.test",
		mapi.PrSenderSmtpAddress:           "room@hermex.test",
	} {
		if got := propStr(answer.Props, tag); got != want {
			t.Errorf("response property %#x = %q, want %q", uint32(tag), got, want)
		}
	}
	ics, _ := inboxCalendarPart(organizer, msgs[0].ID)
	if a := parseAttendee(ics); icalLine(ics, "METHOD") != "REPLY" || a.addr != "room@hermex.test" || a.partstat != "ACCEPTED" {
		t.Errorf("response calendar answers for %q with %q, want a REPLY from room@hermex.test with ACCEPTED:\n%s", a.addr, a.partstat, ics)
	}
	if got := responseOf(t, organizer, tags, recipID); got != ResponseAccepted {
		t.Errorf("organizer's tracking status for the room = %d, want accepted", got)
	}
}

// TestMeetingHookStaysQuietOnSuccess is the control: an ordinary pass must not
// report anything.
func TestMeetingHookStaysQuietOnSuccess(t *testing.T) {
	sink := &hookSink{}
	ok := func(*objectstore.Store, directory.Accounts, *relay.Spool, string, int64) (bool, error) {
		return true, nil
	}

	hook := requestHook(ok, logging.New(sink))
	if handled := hook(nil, nil, "room@hermex.test", 42); !handled {
		t.Errorf("a successful pass did not report the request as handled")
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 0 {
		t.Errorf("a successful pass logged %+v, want nothing", sink.events)
	}
}
