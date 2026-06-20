package meeting

import (
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

var apBase = time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)

// seedRequest files a meeting request in the inbox with a time window, returning its
// store id (the id the delivery path hands the auto-processor).
func seedRequest(t *testing.T, st *objectstore.Store, tags apptTags, start, end time.Time, recurring bool) int64 {
	t.Helper()
	id, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Schedule.Meeting.Request"},
		{Tag: mapi.PrSubject, Value: "Sync"},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "organizer@hermex.test"},
		{Tag: tags.start, Value: mapi.UnixToNTTime(start)},
		{Tag: tags.end, Value: mapi.UnixToNTTime(end)},
		{Tag: tags.recur, Value: recurring},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedAppointment files an existing Calendar appointment with the given busy status.
func seedAppointment(t *testing.T, st *objectstore.Store, tags apptTags, start, end time.Time, busy int32) {
	t.Helper()
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
		{Tag: tags.start, Value: mapi.UnixToNTTime(start)},
		{Tag: tags.end, Value: mapi.UnixToNTTime(end)},
		{Tag: tags.busy, Value: busy},
	}}); err != nil {
		t.Fatal(err)
	}
}

// apSetup opens the room mailbox with the given config and a directory that resolves
// both the room (the responder) and the organizer (so an accept/decline can route the
// organizer notification locally).
func apSetup(t *testing.T, cfg objectstore.MeetingConfig) (*objectstore.Store, apptTags, directory.Accounts) {
	t.Helper()
	roomDir := t.TempDir()
	st, err := objectstore.Open(roomDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SetMeetingConfig(cfg); err != nil {
		t.Fatal(err)
	}
	tags, err := resolveApptTags(st)
	if err != nil {
		t.Fatal(err)
	}
	accounts := directory.StaticAccounts{
		"room@hermex.test":      {MailboxPath: roomDir},
		"organizer@hermex.test": {MailboxPath: t.TempDir()},
	}
	return st, tags, accounts
}

// calBusyStatuses returns the busy status of every Calendar item.
func calBusyStatuses(t *testing.T, st *objectstore.Store, tags apptTags) []int32 {
	t.Helper()
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil {
		t.Fatal(err)
	}
	var out []int32
	for _, o := range objs {
		pv, err := st.GetMessageProperties(o.ID, tags.busy)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, longVal(pv, tags.busy))
	}
	return out
}

// TestAutoProcessMasterOff proves a mailbox with AutoAccept off processes nothing: the
// request is left for manual handling (no calendar item) and the caller is told it was
// not handled (so the out-of-office pass still runs).
func TestAutoProcessMasterOff(t *testing.T) {
	st, tags, accounts := apSetup(t, objectstore.MeetingConfig{AutoAccept: false, DeclineRecurring: true})
	id := seedRequest(t, st, tags, apBase, apBase.Add(time.Hour), false)

	handled, err := AutoProcess(st, accounts, nil, "room@hermex.test", id)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Error("AutoProcess handled a request with AutoAccept off; want not handled")
	}
	if got := calBusyStatuses(t, st, tags); len(got) != 0 {
		t.Errorf("calendar = %v, want empty (no automatic processing)", got)
	}
}

// TestAutoProcessSkipsNonMeeting proves a normal mail is not treated as a meeting
// request even when auto-processing is enabled.
func TestAutoProcessSkipsNonMeeting(t *testing.T) {
	st, _, accounts := apSetup(t, objectstore.MeetingConfig{AutoAccept: true})
	id, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handled, err := AutoProcess(st, accounts, nil, "room@hermex.test", id)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Error("AutoProcess handled a non-meeting message; want not handled")
	}
}

// TestAutoProcessAcceptsConflictFree proves a conflict-free request is auto-accepted:
// it is filed in the calendar as busy and reported handled.
func TestAutoProcessAcceptsConflictFree(t *testing.T) {
	st, tags, accounts := apSetup(t, objectstore.MeetingConfig{AutoAccept: true})
	id := seedRequest(t, st, tags, apBase, apBase.Add(time.Hour), false)

	handled, err := AutoProcess(st, accounts, nil, "room@hermex.test", id)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("AutoProcess did not handle a meeting request")
	}
	got := calBusyStatuses(t, st, tags)
	if len(got) != 1 || got[0] != busyBusy {
		t.Errorf("calendar busy statuses = %v, want [%d] (accepted = busy)", got, busyBusy)
	}
}

// TestAutoProcessDeclinesRecurring proves a recurring request is declined when
// configured: nothing is filed.
func TestAutoProcessDeclinesRecurring(t *testing.T) {
	st, tags, accounts := apSetup(t, objectstore.MeetingConfig{AutoAccept: true, DeclineRecurring: true})
	id := seedRequest(t, st, tags, apBase, apBase.Add(time.Hour), true)

	handled, err := AutoProcess(st, accounts, nil, "room@hermex.test", id)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("AutoProcess did not handle a recurring meeting request")
	}
	if got := calBusyStatuses(t, st, tags); len(got) != 0 {
		t.Errorf("calendar = %v, want empty (recurring request declined)", got)
	}
}

// TestAutoProcessDeclinesConflict proves a conflicting request is declined when
// configured: the conflicting appointment stays and nothing new is filed.
func TestAutoProcessDeclinesConflict(t *testing.T) {
	st, tags, accounts := apSetup(t, objectstore.MeetingConfig{AutoAccept: true, DeclineConflict: true})
	seedAppointment(t, st, tags, apBase, apBase.Add(time.Hour), busyBusy) // existing booking
	id := seedRequest(t, st, tags, apBase.Add(30*time.Minute), apBase.Add(90*time.Minute), false)

	handled, err := AutoProcess(st, accounts, nil, "room@hermex.test", id)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("AutoProcess did not handle a conflicting meeting request")
	}
	if got := calBusyStatuses(t, st, tags); len(got) != 1 {
		t.Errorf("calendar = %v, want only the existing booking (conflicting request declined)", got)
	}
}

// TestAutoProcessTentativeOnConflict proves that when a request conflicts but
// DeclineConflict is off, it is filed tentatively (not accepted, not declined).
func TestAutoProcessTentativeOnConflict(t *testing.T) {
	st, tags, accounts := apSetup(t, objectstore.MeetingConfig{AutoAccept: true}) // no DeclineConflict
	seedAppointment(t, st, tags, apBase, apBase.Add(time.Hour), busyBusy)
	id := seedRequest(t, st, tags, apBase.Add(30*time.Minute), apBase.Add(90*time.Minute), false)

	handled, err := AutoProcess(st, accounts, nil, "room@hermex.test", id)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("AutoProcess did not handle the request")
	}
	got := calBusyStatuses(t, st, tags)
	if len(got) != 2 {
		t.Fatalf("calendar = %v, want 2 (existing booking + tentative filing)", got)
	}
	var tentatives int
	for _, b := range got {
		if b == busyTentative {
			tentatives++
		}
	}
	if tentatives != 1 {
		t.Errorf("calendar busy statuses = %v, want exactly one tentative (%d)", got, busyTentative)
	}
}
