package oxcmail

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
)

const meetingMail = "From: organizer@hermex.test\r\n" +
	"To: alice@hermex.test\r\n" +
	"Subject: Email Subject\r\n" +
	"Content-Type: multipart/alternative; boundary=b\r\n\r\n" +
	"--b\r\nContent-Type: text/plain\r\n\r\nplease come\r\n" +
	"--b\r\nContent-Type: text/calendar; method=REQUEST\r\n\r\n" +
	"BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:m-1\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n" +
	"--b--\r\n"

// TestImportCalendarMerge proves Import overlays a scheduling calendar object's
// class and named appointment properties onto the message — while leaving the
// email's own subject (a regular property the calendar also carries) intact — and
// that a calendar object without a message class is not overlaid at all.
func TestImportCalendarMerge(t *testing.T) {
	startTag := mapi.MakeTag(0x8005, mapi.PtSysTime) // a named appointment property

	scheduling := func(ical []byte) (mapi.PropertyValues, error) {
		if !bytes.Contains(ical, []byte("VCALENDAR")) {
			t.Errorf("importer received non-calendar bytes: %q", ical)
		}
		return mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Schedule.Meeting.Request"},
			{Tag: startTag, Value: uint64(123)},
			// A regular property (id < 0x8000) the email already carries: it must
			// not clobber the email's own value.
			{Tag: mapi.PrSubject, Value: "CALENDAR SUMMARY"},
		}, nil
	}

	msg, err := Import([]byte(meetingMail), Options{CalendarImporter: scheduling})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := msg.Props.Get(mapi.PrMessageClass); c != "IPM.Schedule.Meeting.Request" {
		t.Errorf("class = %v, want IPM.Schedule.Meeting.Request (scheduling class overlaid)", c)
	}
	if !msg.Props.Has(startTag) {
		t.Error("the named appointment property was not overlaid")
	}
	if s, _ := msg.Props.Get(mapi.PrSubject); s != "Email Subject" {
		t.Errorf("subject = %v, want %q (the calendar's regular props must not clobber the email)", s, "Email Subject")
	}

	// A calendar object that carries no message class is a plain VCALENDAR, not a
	// scheduling message: nothing is overlaid.
	plain := func([]byte) (mapi.PropertyValues, error) {
		return mapi.PropertyValues{{Tag: startTag, Value: uint64(123)}}, nil
	}
	msg2, err := Import([]byte(meetingMail), Options{CalendarImporter: plain})
	if err != nil {
		t.Fatal(err)
	}
	if msg2.Props.Has(startTag) {
		t.Error("a non-scheduling calendar object must not overlay appointment props")
	}
	if c, _ := msg2.Props.Get(mapi.PrMessageClass); c != "IPM.Note" {
		t.Errorf("class = %v, want IPM.Note (unchanged for a non-scheduling calendar)", c)
	}

	// Without an importer, the calendar part stays unparsed and becomes an
	// attachment — the message is plain mail.
	msg3, err := Import([]byte(meetingMail), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := msg3.Props.Get(mapi.PrMessageClass); c != "IPM.Note" {
		t.Errorf("class = %v, want IPM.Note (no importer, no overlay)", c)
	}
	if len(msg3.Attachments) != 1 {
		t.Errorf("attachments = %d, want 1 (the unparsed text/calendar part)", len(msg3.Attachments))
	}
}
