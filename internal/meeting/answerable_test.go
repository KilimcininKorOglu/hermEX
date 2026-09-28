package meeting

import (
	"errors"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// TestAnsweringAReplyLeavesTheMeetingAlone proves a meeting response is not a
// message an attendee answers. It carries a calendar part like a request, and
// "accepting" one used to overwrite the organizer's own meeting with the response's
// properties, marking it a meeting received from someone else.
func TestAnsweringAReplyLeavesTheMeetingAlone(t *testing.T) {
	st, tags, _ := organizerWithEvent(t, "answer-1", "bob@hermex.test")
	cal, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil || len(cal) != 1 {
		t.Fatalf("calendar = %v (%v), want the organizer's meeting", cal, err)
	}
	replyID := deliverReply(t, st, "answer-1", "bob@hermex.test", "ACCEPTED")

	_, err = RespondOnBehalf(st, nil, nil, "organizer@hermex.test", "organizer@hermex.test", replyID, ResponseAccepted, false)
	if !errors.Is(err, ErrNotARequest) {
		t.Fatalf("answering a reply: err = %v, want ErrNotARequest", err)
	}
	props, err := st.GetMessageProperties(cal[0].ID, tags.State, tags.Resp)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 0 {
		t.Errorf("the organizer's meeting gained %v, want it untouched", props)
	}
}

// TestAnsweringTheOwnInvitationIsRefused proves the organizer cannot answer the
// invitation it sent. The copy kept in Sent Items is a meeting request like the one
// each attendee received, and accepting it used to overwrite the organizer's
// meeting with the request's properties, mark it received from someone else, and
// send the organizer a response from themselves.
func TestAnsweringTheOwnInvitationIsRefused(t *testing.T) {
	st, tags, _ := organizerWithEvent(t, "own-1", "bob@hermex.test")
	cal, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil || len(cal) != 1 {
		t.Fatalf("calendar = %v (%v), want the organizer's meeting", cal, err)
	}
	sentID, err := st.CreateMessage(int64(mapi.PrivateFIDSentItems), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: requestClass},
		{Tag: tags.UID, Value: "own-1"},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "organizer@hermex.test"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Respond(st, nil, nil, "organizer@hermex.test", sentID, ResponseAccepted, false); !errors.Is(err, ErrOrganizer) {
		t.Fatalf("answering the own invitation: err = %v, want ErrOrganizer", err)
	}
	props, err := st.GetMessageProperties(cal[0].ID, tags.State, tags.Resp)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 0 {
		t.Errorf("the organizer's meeting gained %v, want it untouched", props)
	}
}

// TestAnsweringACalendarItemNeedsAnInvitation proves a calendar item is answered
// only when it is a meeting the mailbox was invited to and is still on
// ([MS-OXOCAL] 2.2.1.10): the organizer's own meeting and a canceled one are
// refused.
func TestAnsweringACalendarItemNeedsAnInvitation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state int32
		want  error
	}{
		{"organized here", asfMeeting, ErrOrganizer},
		{"canceled", asfMeeting | asfReceived | asfCanceled, ErrCanceled},
		{"invited", asfMeeting | asfReceived, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := objectstore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			tags, err := ResolveTags(st)
			if err != nil {
				t.Fatal(err)
			}
			id, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: mapi.PropertyValues{
				{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
				{Tag: tags.UID, Value: "answer-2"},
				{Tag: tags.State, Value: tc.state},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Respond(st, nil, nil, "alice@hermex.test", id, ResponseTentative, false); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
