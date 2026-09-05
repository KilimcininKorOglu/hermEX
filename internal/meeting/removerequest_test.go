package meeting

import (
	"errors"
	"hermex/internal/directory"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// appendRequest puts a real, IMAP-indexed meeting request in the Inbox. The request
// has to go through AppendMessage rather than CreateMessage: only an indexed message
// has the (folder, UID) a move is keyed by.
func appendRequest(t *testing.T, st *objectstore.Store) int64 {
	t.Helper()
	raw := []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Sync\r\n" +
		"Date: Mon, 01 Jun 2026 10:00:00 +0000\r\n\r\nplease come\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ModifyMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Schedule.Meeting.Request"},
	}); err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// folderCount counts a folder's live objects.
func folderCount(t *testing.T, st *objectstore.Store, fid int64) int {
	t.Helper()
	objs, err := st.ListFolderObjects(fid)
	if err != nil {
		t.Fatal(err)
	}
	return len(objs)
}

// TestRespondKeepsRequestByDefault pins the default. A mailbox that has not asked
// for the cleanup must still find the answered request where it arrived.
func TestRespondKeepsRequestByDefault(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	reqID := appendRequest(t, st)
	if _, err := Respond(st, nil, nil, "alice@hermex.test", reqID, ResponseAccepted, false); err != nil {
		t.Fatal(err)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("Inbox holds %d messages, want the request still there", n)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDDeletedItems)); n != 0 {
		t.Errorf("Deleted Items holds %d messages, want 0", n)
	}
}

// TestRespondRemovesRequestWhenConfigured is the feature. With the mailbox flag set,
// answering a meeting request also files the request mail away, so an answered
// request does not sit in the Inbox forever.
func TestRespondRemovesRequestWhenConfigured(t *testing.T) {
	for _, response := range []int32{ResponseAccepted, ResponseTentative, ResponseDeclined} {
		t.Run(responseName(response), func(t *testing.T) {
			st, err := objectstore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.SetMeetingConfig(objectstore.MeetingConfig{RemoveRequestOnResponse: true}); err != nil {
				t.Fatal(err)
			}

			reqID := appendRequest(t, st)
			if _, err := Respond(st, nil, nil, "alice@hermex.test", reqID, response, false); err != nil {
				t.Fatal(err)
			}
			if n := folderCount(t, st, int64(mapi.PrivateFIDInbox)); n != 0 {
				t.Errorf("Inbox holds %d messages, want the request moved out", n)
			}
			if n := folderCount(t, st, int64(mapi.PrivateFIDDeletedItems)); n != 1 {
				t.Errorf("Deleted Items holds %d messages, want the request", n)
			}
		})
	}
}

// TestRespondSurvivesAnUnindexedRequest keeps the cleanup off an object that has no
// IMAP identity. A calendar or contact item never enters the index, so there is no
// (folder, UID) to move and the response must still succeed.
func TestRespondSurvivesAnUnindexedRequest(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMeetingConfig(objectstore.MeetingConfig{RemoveRequestOnResponse: true}); err != nil {
		t.Fatal(err)
	}

	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Schedule.Meeting.Request"},
			{Tag: tags.UID, Value: "unindexed"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Respond(st, nil, nil, "alice@hermex.test", reqID, ResponseAccepted, false); err != nil {
		t.Fatalf("responding to an unindexed request failed: %v", err)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("Inbox holds %d objects, want the unindexed request left alone", n)
	}
}

// TestMeetingConfigRoundTripsTheFlag pins the new field through the store, because
// it rides a private named property rather than one of the PR_SCHDINFO_* tags.
func TestMeetingConfigRoundTripsTheFlag(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if cfg, err := st.GetMeetingConfig(); err != nil || cfg.RemoveRequestOnResponse {
		t.Fatalf("a fresh store reports %+v, err %v; want the flag off", cfg, err)
	}
	want := objectstore.MeetingConfig{AutoAccept: true, DeclineConflict: true, RemoveRequestOnResponse: true}
	if err := st.SetMeetingConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetMeetingConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("config = %+v, want %+v", got, want)
	}
	// Clearing it must stick too, so the setting can be turned back off.
	if err := st.SetMeetingConfig(objectstore.MeetingConfig{}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetMeetingConfig(); err != nil || got.RemoveRequestOnResponse {
		t.Errorf("after clearing: %+v, err %v; want the flag off", got, err)
	}
}

func responseName(r int32) string {
	switch r {
	case ResponseAccepted:
		return "accepted"
	case ResponseTentative:
		return "tentative"
	default:
		return "declined"
	}
}

// TestRespondSurvivesAFailedCleanup is the ordering guarantee. The cleanup runs
// after the response is recorded, the appointment booked and the organizer told, so
// a failure there must not report failure for work that already completed: a client
// that retried would send the organizer a second reply.
func TestRespondSurvivesAFailedCleanup(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMeetingConfig(objectstore.MeetingConfig{RemoveRequestOnResponse: true}); err != nil {
		t.Fatal(err)
	}
	removeHook = func() error { return errors.New("the mailbox refused the move") }
	t.Cleanup(func() { removeHook = nil })

	reqID := appendRequest(t, st)
	if _, err := Respond(st, nil, nil, "alice@hermex.test", reqID, ResponseAccepted, false); err != nil {
		t.Fatalf("a failed cleanup failed the whole response: %v", err)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("Inbox holds %d messages, want the request left where it was", n)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDCalendar)); n != 1 {
		t.Errorf("Calendar holds %d appointments, want the booking to have stood", n)
	}
}

// seedAppointmentFor stores a Calendar appointment carrying the meeting's UID, the
// object a calendar-view response answers.
func seedAppointmentFor(t *testing.T, st *objectstore.Store, tags Tags, uid string) int64 {
	t.Helper()
	raw := []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Sync\r\n" +
		"Date: Mon, 01 Jun 2026 10:00:00 +0000\r\n\r\nbody\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDCalendar), raw, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ModifyMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
		{Tag: tags.UID, Value: uid},
	}); err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// appendRequestWithUID is appendRequest with the meeting's UID stamped on, so the
// calendar-view path can find it.
func appendRequestWithUID(t *testing.T, st *objectstore.Store, tags Tags, uid string) int64 {
	t.Helper()
	id := appendRequest(t, st)
	if err := st.ModifyMessageProperties(id, mapi.PropertyValues{{Tag: tags.UID, Value: uid}}); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestRespondFromCalendarNeverFilesTheAppointment is the guard. The answered object
// is the appointment when the user responds from the calendar, so moving the
// answered object would take the appointment off the calendar and into Deleted
// Items, which is the opposite of what accepting a meeting means.
func TestRespondFromCalendarNeverFilesTheAppointment(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMeetingConfig(objectstore.MeetingConfig{RemoveRequestOnResponse: true}); err != nil {
		t.Fatal(err)
	}
	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	apptID := seedAppointmentFor(t, st, tags, "no-request-here")

	if _, err := Respond(st, nil, nil, "alice@hermex.test", apptID, ResponseAccepted, false); err != nil {
		t.Fatal(err)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDDeletedItems)); n != 0 {
		t.Errorf("Deleted Items holds %d objects, want 0: the appointment itself was filed away", n)
	}
}

// TestRespondFromCalendarRemovesTheRequest is the consistency half. A mailbox must
// not end up in a different state depending on which view the user answered from,
// so a calendar response finds the invitation in the Inbox by the meeting's UID and
// files that away instead.
func TestRespondFromCalendarRemovesTheRequest(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMeetingConfig(objectstore.MeetingConfig{RemoveRequestOnResponse: true}); err != nil {
		t.Fatal(err)
	}
	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	const uid = "meeting-uid-1"
	appendRequestWithUID(t, st, tags, uid)
	apptID := seedAppointmentFor(t, st, tags, uid)

	if _, err := Respond(st, nil, nil, "alice@hermex.test", apptID, ResponseAccepted, false); err != nil {
		t.Fatal(err)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDInbox)); n != 0 {
		t.Errorf("Inbox holds %d messages, want the request moved out", n)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDDeletedItems)); n != 1 {
		t.Errorf("Deleted Items holds %d messages, want just the request", n)
	}
	if _, err := st.OpenMessage(apptID); err != nil {
		t.Errorf("the appointment is gone: %v", err)
	}
}

// TestRespondFromCalendarLeavesOtherMeetings keeps the UID lookup honest: an
// invitation for a different meeting must not be swept up.
func TestRespondFromCalendarLeavesOtherMeetings(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMeetingConfig(objectstore.MeetingConfig{RemoveRequestOnResponse: true}); err != nil {
		t.Fatal(err)
	}
	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	appendRequestWithUID(t, st, tags, "answered-meeting")
	appendRequestWithUID(t, st, tags, "some-other-meeting")
	apptID := seedAppointmentFor(t, st, tags, "answered-meeting")

	if _, err := Respond(st, nil, nil, "alice@hermex.test", apptID, ResponseAccepted, false); err != nil {
		t.Fatal(err)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("Inbox holds %d messages, want the other meeting's invitation left alone", n)
	}
}

// TestRespondFromCalendarRemovesEveryInvitation covers the updated meeting. An
// organizer who changes a meeting sends another invitation, so the Inbox holds more
// than one for the same UID. Filing only the first away leaves an answered
// invitation sitting there, which is the state this setting exists to avoid.
func TestRespondFromCalendarRemovesEveryInvitation(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMeetingConfig(objectstore.MeetingConfig{RemoveRequestOnResponse: true}); err != nil {
		t.Fatal(err)
	}
	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	const uid = "updated-meeting"
	appendRequestWithUID(t, st, tags, uid)
	appendRequestWithUID(t, st, tags, uid) // the organizer's update
	appendRequestWithUID(t, st, tags, "another-meeting")
	apptID := seedAppointmentFor(t, st, tags, uid)

	if _, err := Respond(st, nil, nil, "alice@hermex.test", apptID, ResponseAccepted, false); err != nil {
		t.Fatal(err)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("Inbox holds %d messages, want only the other meeting's invitation", n)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDDeletedItems)); n != 2 {
		t.Errorf("Deleted Items holds %d messages, want both invitations for the answered meeting", n)
	}
}

// TestAutoProcessKeepsTheRequestMail is the line between a response the reader
// gave and one the server decided. Auto-processing books the meeting before
// anyone has opened the invitation, so clearing the mail there would take away a
// message the reader has never seen and cannot find again.
func TestAutoProcessKeepsTheRequestMail(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMeetingConfig(objectstore.MeetingConfig{
		AutoAccept:              true,
		RemoveRequestOnResponse: true,
	}); err != nil {
		t.Fatal(err)
	}

	// The organizer notification needs the two mailboxes resolvable; the spool is
	// nil, which the auto-process tests already rely on.
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: t.TempDir()},
		"bob@hermex.test":   {MailboxPath: t.TempDir()},
	}
	reqID := appendRequest(t, st)
	handled, err := AutoProcess(st, accounts, nil, "alice@hermex.test", reqID)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("the request was not auto-processed, so this proves nothing")
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("Inbox holds %d messages, want the request left where the reader can see it", n)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDDeletedItems)); n != 0 {
		t.Errorf("Deleted Items holds %d messages, want none", n)
	}

	// The reader's own answer still clears it, so the setting is not simply off.
	if _, err := Respond(st, accounts, nil, "alice@hermex.test", reqID, ResponseAccepted, false); err != nil {
		t.Fatal(err)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDInbox)); n != 0 {
		t.Errorf("after the reader answered, the Inbox still holds %d messages", n)
	}
}
