package meeting

import (
	"bytes"
	"net/mail"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// headerValue reads one header of a delivered message.
func headerValue(t *testing.T, raw []byte, name string) string {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return msg.Header.Get(name)
}

// TestResponseSubjectNamesTheAnswer follows a room's automatic acceptance of an
// invitation delivered as mail to the organizer. The response's Subject header is
// the meeting's subject under the prefix that names the answer, and a forwarded
// invitation is answered under the meeting's own subject, not the forward's
// ([MS-OXOCAL] Accepting the Meeting Request). The delivered request stores its
// subject prefix and normalized subject, which the Subject header is built from, so
// a response that changed the full subject alone went out under the request's own
// subject with no answer in it.
func TestResponseSubjectNamesTheAnswer(t *testing.T) {
	for _, subject := range []string{"Sync", "FW: Sync"} {
		t.Run(subject, func(t *testing.T) {
			room, _, _ := apSetup(t, objectstore.MeetingConfig{AutoAccept: true})
			organizer, _, _ := organizerWithEvent(t, "subject-1", "room@hermex.test")
			accounts := directory.StaticAccounts{
				"room@hermex.test":      {MailboxPath: room.Dir()},
				"organizer@hermex.test": {MailboxPath: organizer.Dir()},
			}
			useDeliveryHooks(t)
			invite := []byte("From: organizer@hermex.test\r\nTo: room@hermex.test\r\nSubject: " + subject + "\r\n" +
				"MIME-Version: 1.0\r\nContent-Type: text/calendar; method=REQUEST; charset=UTF-8\r\n\r\n" +
				"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//test//EN\r\nMETHOD:REQUEST\r\n" +
				"BEGIN:VEVENT\r\nUID:subject-1\r\nDTSTAMP:20260619T090000Z\r\n" +
				"DTSTART:20260620T100000Z\r\nDTEND:20260620T110000Z\r\nSUMMARY:Sync\r\n" +
				"ORGANIZER:mailto:organizer@hermex.test\r\nATTENDEE:mailto:room@hermex.test\r\n" +
				"END:VEVENT\r\nEND:VCALENDAR\r\n")
			if _, err := mta.DeliverAndRelay(accounts, nil, "organizer@hermex.test", []string{"room@hermex.test"}, invite, apBase); err != nil {
				t.Fatal(err)
			}

			raw, _ := onlyInboxMessage(t, organizer)
			if got := headerValue(t, raw, "Subject"); got != "Accepted: Sync" {
				t.Errorf("response Subject = %q, want %q", got, "Accepted: Sync")
			}
		})
	}
}
