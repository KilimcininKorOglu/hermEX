package meeting

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// TestProcessReplyResolvesTheMessageID proves tracking still works once a mailbox
// holds a non-mail object. The delivery pass hands an object-store message id, and
// a calendar item consumes an id without an IMAP UID, so from the first
// appointment onward the two numbers differ and reading the raw form by the id
// finds the wrong message, or none at all.
func TestProcessReplyResolvesTheMessageID(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	mustNoErr(t, err, "Open")
	defer st.Close()

	tags, err := ResolveTags(st)
	mustNoErr(t, err, "ResolveTags")
	eventID := seedTrackedEvent(t, st, tags)
	info := appendTrackedReply(t, st)
	if int64(info.UID) == info.ID {
		t.Fatalf("the seeding did not separate id %d from UID %d, so the case is not covered", info.ID, info.UID)
	}

	handled, _ := ProcessReply(st, "bob@hermex.test", info.ID)
	wantTrue(t, handled, "the REPLY was processed")
	wantEq(t, trackedResponse(t, st, tags, eventID), int32(ResponseAccepted), "the attendee's tracking status")
}

// seedTrackedEvent creates the organizer's event. Creating it first is what pushes
// the reply's message id past its UID.
func seedTrackedEvent(t *testing.T, st *objectstore.Store, tags Tags) int64 {
	t.Helper()
	id, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
			{Tag: tags.UID, Value: "tracked-meeting"},
		},
		Recipients: []mapi.PropertyValues{{
			{Tag: mapi.PrSmtpAddress, Value: "bob@hermex.test"},
			{Tag: mapi.PrDisplayName, Value: "Bob"},
		}},
	})
	mustNoErr(t, err, "CreateMessage")
	return id
}

// appendTrackedReply appends the attendee's iTIP REPLY to the Inbox.
func appendTrackedReply(t *testing.T, st *objectstore.Store) objectstore.MessageInfo {
	t.Helper()
	raw := "From: bob@hermex.test\r\nTo: organizer@hermex.test\r\nSubject: Accepted\r\n" +
		"Content-Type: text/calendar; method=REPLY\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:tracked-meeting\r\n" +
		"ATTENDEE;PARTSTAT=ACCEPTED:mailto:bob@hermex.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1718200000, 0), 0)
	mustNoErr(t, err, "AppendMessage")
	return info
}

// trackedResponse reads the tracking status stored on the event's one attendee.
func trackedResponse(t *testing.T, st *objectstore.Store, tags Tags, eventID int64) int32 {
	t.Helper()
	recips, err := st.ListRecipients(eventID)
	mustNoErr(t, err, "ListRecipients")
	mustCount(t, len(recips), 1, "the event recipients")
	props, err := st.GetRecipientProperties(recips[0].ID)
	mustNoErr(t, err, "GetRecipientProperties")
	v, ok := props.Get(tags.Resp)
	if !ok {
		t.Fatal("the attendee's tracking status was never written")
	}
	n, _ := v.(int32)
	return n
}
