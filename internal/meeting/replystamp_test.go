package meeting

import (
	"regexp"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

var dtstampLine = regexp.MustCompile(`(?m)^DTSTAMP:(\d{8}T\d{6}Z)\r?$`)

// TestReplyIsStampedWhenItIsSent proves a response's DTSTAMP is when the attendee
// answered ([MS-OXCICAL] DTSTAMP exports PidLidAttendeeCriticalChange for a
// REPLY), not the request's stamp and not the meeting's start: the organizer keeps
// the newest of several answers by it (RFC 5546 section 2.1.5).
func TestReplyIsStampedWhenItIsSent(t *testing.T) {
	room, _, _ := apSetup(t, objectstore.MeetingConfig{AutoAccept: true})
	organizer, _, _ := organizerWithEvent(t, "stamp-1", "room@hermex.test")
	accounts := directory.StaticAccounts{
		"room@hermex.test":      {MailboxPath: room.Dir()},
		"organizer@hermex.test": {MailboxPath: organizer.Dir()},
	}
	useDeliveryHooks(t)
	invite := []byte("From: organizer@hermex.test\r\nTo: room@hermex.test\r\nSubject: Sync\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/calendar; method=REQUEST; charset=UTF-8\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//test//EN\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:stamp-1\r\nDTSTAMP:20260619T090000Z\r\n" +
		"DTSTART:20260620T100000Z\r\nDTEND:20260620T110000Z\r\nSUMMARY:Sync\r\n" +
		"ORGANIZER:mailto:organizer@hermex.test\r\nATTENDEE:mailto:room@hermex.test\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n")
	before := time.Now().UTC().Add(-time.Minute)
	if _, err := mta.DeliverAndRelay(accounts, nil, "organizer@hermex.test", []string{"room@hermex.test"}, invite, apBase); err != nil {
		t.Fatal(err)
	}

	raw, _ := onlyInboxMessage(t, organizer)
	m := dtstampLine.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("the reply carries no DTSTAMP:\n%s", raw)
	}
	stamp, err := time.Parse("20060102T150405Z", string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	if stamp.Before(before) {
		t.Errorf("reply DTSTAMP = %v, want the time it was sent (after %v)", stamp, before)
	}
}
