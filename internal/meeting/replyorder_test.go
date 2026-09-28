package meeting

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// deliverStampedReply appends an iTIP REPLY carrying the DTSTAMP the attendee
// sent it with, and returns its id.
func deliverStampedReply(t *testing.T, st *objectstore.Store, uid, attendee, partstat, dtstamp string) int64 {
	t.Helper()
	raw := "From: " + attendee + "\r\nTo: organizer@hermex.test\r\nSubject: Reply\r\n" +
		"Content-Type: text/calendar; method=REPLY\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\n" +
		"DTSTAMP:" + dtstamp + "\r\n" +
		"ATTENDEE;PARTSTAT=" + partstat + ":mailto:" + attendee + "\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// TestOlderReplyDoesNotOverwriteANewerOne proves the organizer keeps the newest of
// an attendee's answers when they arrive out of order ([MS-OXOCAL] 3.1.4.8.5.2;
// RFC 5546 2.1.5): a decline sent after an accept stands even when the accept is
// delivered last, and the recorded time is when the decline was sent.
func TestOlderReplyDoesNotOverwriteANewerOne(t *testing.T) {
	const attendee = "bob@hermex.test"
	st, tags, recipID := organizerWithEvent(t, "order-1", attendee)

	newer := deliverStampedReply(t, st, "order-1", attendee, "DECLINED", "20260610T120000Z")
	if _, err := ProcessReply(st, nil, attendee, newer); err != nil {
		t.Fatal(err)
	}
	older := deliverStampedReply(t, st, "order-1", attendee, "ACCEPTED", "20260610T090000Z")
	if _, err := ProcessReply(st, nil, attendee, older); err != nil {
		t.Fatal(err)
	}

	if got := responseOf(t, st, tags, recipID); got != ResponseDeclined {
		t.Errorf("tracking status = %d, want the newer decline (%d)", got, ResponseDeclined)
	}
	row, err := st.GetRecipientProperties(recipID, mapi.PrRecipientTrackStatusTime)
	if err != nil {
		t.Fatal(err)
	}
	want := mapi.UnixToNTTime(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))
	if got, _ := row.Get(mapi.PrRecipientTrackStatusTime); got != want {
		t.Errorf("track status time = %v, want the decline's DTSTAMP %v", got, want)
	}

	// Outlook reads the same response from PidTagRecipientTrackStatus.
	row, err = st.GetRecipientProperties(recipID, mapi.PrRecipientTrackStatus)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := row.Get(mapi.PrRecipientTrackStatus); got != ResponseDeclined {
		t.Errorf("PidTagRecipientTrackStatus = %v, want %d", got, ResponseDeclined)
	}

	// A later answer still replaces it.
	latest := deliverStampedReply(t, st, "order-1", attendee, "TENTATIVE", "20260611T080000Z")
	if _, err := ProcessReply(st, nil, attendee, latest); err != nil {
		t.Fatal(err)
	}
	if got := responseOf(t, st, tags, recipID); got != ResponseTentative {
		t.Errorf("tracking status = %d, want the latest tentative (%d)", got, ResponseTentative)
	}
}
