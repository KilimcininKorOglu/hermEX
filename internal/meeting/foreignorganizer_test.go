package meeting

import (
	"errors"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// movedEvent is the meeting at 25 June instead of 20 June, the change a hijacker
// would make.
const movedEvent = "DTSTART:20260625T100000Z\r\nDTEND:20260625T110000Z\r\nSEQUENCE:5\r\n"

// meetingStart reads the start of the room's one calendar item.
func meetingStart(t *testing.T, st *objectstore.Store) time.Time {
	t.Helper()
	at, err := resolveApptTags(st)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil || len(objs) != 1 {
		t.Fatalf("calendar holds %d items (err %v), want the one meeting", len(objs), err)
	}
	pv, err := st.GetMessageProperties(objs[0].ID, at.start)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := pv.Get(at.start)
	n, ok := v.(uint64)
	if !ok {
		t.Fatal("the meeting has no start")
	}
	return mapi.NTTimeToUnix(n).UTC()
}

// deliverForged delivers a REQUEST whose From header names fromHeader while the
// envelope sender is envelope.
func deliverForged(t *testing.T, st *objectstore.Store, accounts directory.Accounts, envelope, fromHeader string) {
	t.Helper()
	raw := "From: " + fromHeader + "\r\nTo: room@hermex.test\r\nSubject: REQUEST\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/calendar; method=REQUEST; charset=UTF-8\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//test//EN\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:" + cancelUID + "\r\nDTSTAMP:20260619T090000Z\r\nSUMMARY:Sync\r\n" +
		"ORGANIZER:mailto:organizer@hermex.test\r\nATTENDEE:mailto:room@hermex.test\r\n" + movedEvent +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), apBase, 0)
	if err != nil {
		t.Fatal(err)
	}
	mta.OnMeetingRequest(st, accounts, envelope, "room@hermex.test", info.ID)
}

// TestARequestFromAnotherSenderCannotMoveTheMeeting follows RFC 5546 3.2.2: only
// the organizer updates a meeting. The UID alone let anyone who knew it move a
// meeting a room had accepted and take it over as organizer, by a request from
// their own address or by one whose From names the organizer.
func TestARequestFromAnotherSenderCannotMoveTheMeeting(t *testing.T) {
	want := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	t.Run("own address", func(t *testing.T) {
		st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
		deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", singleEvent("0"))
		deliverScheduling(t, accounts, "mallory@evil.example", "REQUEST", movedEvent)
		if got := meetingStart(t, st); !got.Equal(want) {
			t.Errorf("the meeting moved to %v", got)
		}
	})
	t.Run("forged From", func(t *testing.T) {
		st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
		deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", singleEvent("0"))
		deliverForged(t, st, accounts, "mallory@evil.example", "organizer@hermex.test")
		if got := meetingStart(t, st); !got.Equal(want) {
			t.Errorf("the meeting moved to %v", got)
		}
	})
	t.Run("organizer, other case", func(t *testing.T) {
		st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
		deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", singleEvent("0"))
		deliverScheduling(t, accounts, "Organizer@HERMEX.test", "REQUEST", movedEvent)
		if got := meetingStart(t, st); !got.Equal(time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)) {
			t.Errorf("the organizer's update left the meeting at %v", got)
		}
	})
}

// TestDecliningAForeignRequestKeepsTheMeeting answers a request from another
// sender by hand: a decline used to remove the meeting the calendar holds under
// the same UID.
func TestDecliningAForeignRequestKeepsTheMeeting(t *testing.T) {
	st, accounts := cancelHarness(t, objectstore.MeetingConfig{})
	deliverScheduling(t, accounts, "organizer@hermex.test", "REQUEST", singleEvent("0"))
	deliverScheduling(t, accounts, "mallory@evil.example", "REQUEST", movedEvent)
	_, err := Respond(st, nil, nil, "room@hermex.test", lastInboxID(t, st), ResponseDeclined, false)
	if !errors.Is(err, ErrForeignOrganizer) || !errors.Is(err, ErrNotARequest) {
		t.Errorf("declining a foreign request: err = %v, want ErrForeignOrganizer", err)
	}
	meetingStart(t, st)
}
