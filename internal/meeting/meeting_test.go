package meeting

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// TestRespondDeclineAfterAccept proves that declining a meeting a prior accept put
// on the calendar removes that appointment — matching the reference's doDecline,
// which deletes the calendar items carrying the meeting's UID. A meeting you decline
// must not linger on your calendar.
func TestRespondDeclineAfterAccept(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Schedule.Meeting.Request"},
			{Tag: tags.UID, Value: "decline-me"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Accept files the appointment (no organizer notification: send is false).
	if _, err := Respond(st, nil, nil, "alice@hermex.test", reqID, ResponseAccepted, false); err != nil {
		t.Fatal(err)
	}
	if cal, _ := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar)); len(cal) != 1 {
		t.Fatalf("after accept: calendar = %d items, want 1 (the appointment)", len(cal))
	}

	// Declining removes it.
	if _, err := Respond(st, nil, nil, "alice@hermex.test", reqID, ResponseDeclined, false); err != nil {
		t.Fatal(err)
	}
	if cal, _ := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar)); len(cal) != 0 {
		t.Errorf("after decline: calendar = %d items, want 0 (the appointment was removed)", len(cal))
	}
}
