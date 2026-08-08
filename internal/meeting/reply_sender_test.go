package meeting

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// organizerWithEvent builds an organizer's calendar event for uid with one
// attendee, and returns the store plus the attendee's recipient id.
func organizerWithEvent(t *testing.T, uid, attendee string) (*objectstore.Store, Tags, int64) {
	t.Helper()
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
			{Tag: tags.UID, Value: uid},
		},
		Recipients: []mapi.PropertyValues{{
			{Tag: mapi.PrSmtpAddress, Value: attendee},
			{Tag: mapi.PrDisplayName, Value: attendee},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	recips, err := st.ListRecipients(eventID)
	if err != nil || len(recips) != 1 {
		t.Fatalf("recipients = %v (%v), want one attendee", recips, err)
	}
	return st, tags, recips[0].ID
}

// deliverReply appends an iTIP REPLY to the organizer's inbox and returns the id
// the delivery pass would hand the processor.
func deliverReply(t *testing.T, st *objectstore.Store, uid, attendee, partstat string) int64 {
	t.Helper()
	raw := "From: someone@hermex.test\r\n" +
		"To: organizer@hermex.test\r\n" +
		"Subject: Accepted\r\n" +
		"Content-Type: text/calendar; method=REPLY\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\n" +
		"ATTENDEE;PARTSTAT=" + partstat + ":mailto:" + attendee + "\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1718200000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// responseOf reads the attendee's recorded tracking status.
func responseOf(t *testing.T, st *objectstore.Store, tags Tags, recipID int64) int32 {
	t.Helper()
	props, err := st.GetRecipientProperties(recipID)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := props.Get(tags.Resp)
	if !ok {
		return 0
	}
	n, _ := v.(int32)
	return n
}

// TestProcessReplyRefusesForgedAttendee proves a REPLY cannot set someone else's
// tracking status. The ATTENDEE line is body content, so any invitee who knows the
// meeting UID could otherwise mail the organizer and answer on a co-invitee's
// behalf.
func TestProcessReplyRefusesForgedAttendee(t *testing.T) {
	st, tags, recipID := organizerWithEvent(t, "meeting-1", "bob@hermex.test")
	msgID := deliverReply(t, st, "meeting-1", "bob@hermex.test", "ACCEPTED")

	if ProcessReply(st, "mallory@hermex.test", msgID) {
		t.Error("a REPLY from another sender was processed")
	}
	if got := responseOf(t, st, tags, recipID); got != 0 {
		t.Errorf("bob's tracking status = %d, want it untouched", got)
	}
}

// TestProcessReplyAcceptsTheAttendeesOwnReply keeps genuine tracking working.
func TestProcessReplyAcceptsTheAttendeesOwnReply(t *testing.T) {
	st, tags, recipID := organizerWithEvent(t, "meeting-2", "bob@hermex.test")
	msgID := deliverReply(t, st, "meeting-2", "bob@hermex.test", "ACCEPTED")

	if !ProcessReply(st, "Bob@Hermex.Test", msgID) {
		t.Fatal("the attendee's own REPLY was not processed")
	}
	if got := responseOf(t, st, tags, recipID); got != ResponseAccepted {
		t.Errorf("tracking status = %d, want accepted (%d)", got, ResponseAccepted)
	}
}

// TestProcessReplyRefusesEmptySender covers the bounce path: a null return-path
// proves nothing about who answered.
func TestProcessReplyRefusesEmptySender(t *testing.T) {
	st, tags, recipID := organizerWithEvent(t, "meeting-3", "bob@hermex.test")
	msgID := deliverReply(t, st, "meeting-3", "bob@hermex.test", "DECLINED")

	if ProcessReply(st, "", msgID) {
		t.Error("a REPLY with no envelope sender was processed")
	}
	if got := responseOf(t, st, tags, recipID); got != 0 {
		t.Errorf("tracking status = %d, want it untouched", got)
	}
}
