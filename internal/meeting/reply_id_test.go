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
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	// The organizer's event. Creating it first is what pushes the reply's message
	// id past its UID.
	eventID, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
			{Tag: tags.UID, Value: "tracked-meeting"},
		},
		Recipients: []mapi.PropertyValues{{
			{Tag: mapi.PrSmtpAddress, Value: "bob@hermex.test"},
			{Tag: mapi.PrDisplayName, Value: "Bob"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	raw := "From: bob@hermex.test\r\nTo: organizer@hermex.test\r\nSubject: Accepted\r\n" +
		"Content-Type: text/calendar; method=REPLY\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:tracked-meeting\r\n" +
		"ATTENDEE;PARTSTAT=ACCEPTED:mailto:bob@hermex.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1718200000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if int64(info.UID) == info.ID {
		t.Fatalf("the seeding did not separate id %d from UID %d, so the case is not covered", info.ID, info.UID)
	}

	if !ProcessReply(st, info.ID) {
		t.Fatal("the REPLY was not processed")
	}
	recips, err := st.ListRecipients(eventID)
	if err != nil || len(recips) != 1 {
		t.Fatalf("recipients = %v (%v)", recips, err)
	}
	props, err := st.GetRecipientProperties(recips[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := props.Get(tags.Resp)
	if !ok {
		t.Fatal("the attendee's tracking status was never written")
	}
	if n, _ := v.(int32); n != ResponseAccepted {
		t.Errorf("tracking status = %v, want accepted (%d)", v, ResponseAccepted)
	}
}
