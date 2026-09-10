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
// class and named appointment properties onto the message, while leaving the
// email's own subject (a regular property the calendar also carries) intact, and
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
	mustNoErr(t, err, "import with a scheduling importer")
	wantEq(t, propString(msg.Props, mapi.PrMessageClass), "IPM.Schedule.Meeting.Request", "overlaid message class")
	wantTrue(t, msg.Props.Has(startTag), "the named appointment property is overlaid")
	wantEq(t, propString(msg.Props, mapi.PrSubject), "Email Subject",
		"subject (the calendar's regular props must not clobber the email)")

	// A calendar object that carries no message class is a plain VCALENDAR, not a
	// scheduling message: nothing is overlaid.
	plain := func([]byte) (mapi.PropertyValues, error) {
		return mapi.PropertyValues{{Tag: startTag, Value: uint64(123)}}, nil
	}
	msg2, err := Import([]byte(meetingMail), Options{CalendarImporter: plain})
	mustNoErr(t, err, "import with a non-scheduling importer")
	wantFalse(t, msg2.Props.Has(startTag), "a non-scheduling calendar object overlays appointment props")
	wantEq(t, propString(msg2.Props, mapi.PrMessageClass), "IPM.Note", "class for a non-scheduling calendar")

	// Without an importer, the calendar part stays unparsed and becomes an
	// attachment, the message is plain mail.
	msg3, err := Import([]byte(meetingMail), Options{})
	mustNoErr(t, err, "import with no importer")
	wantEq(t, propString(msg3.Props, mapi.PrMessageClass), "IPM.Note", "class with no importer")
	wantEq(t, len(msg3.Attachments), 1, "attachments with no importer (the unparsed text/calendar part)")
}

// TestImportCarriesTheVerbatimCalendar is what a delivered recurring invitation
// needs. A recurring event is preserved as bytes rather than synthesized, so an
// overlay that dropped those bytes would store the series as its first instance
// alone and every reader serving the stored body would show one event.
func TestImportCarriesTheVerbatimCalendar(t *testing.T) {
	body := []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nRRULE:FREQ=WEEKLY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	recurring := func([]byte) (mapi.PropertyValues, error) {
		return mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Schedule.Meeting.Request"},
			{Tag: mapi.PrIcalOriginal, Value: body},
		}, nil
	}

	msg, err := Import([]byte(meetingMail), Options{CalendarImporter: recurring})
	mustNoErr(t, err, "import a recurring invitation")

	v, ok := msg.Props.Get(mapi.PrIcalOriginal)
	if !ok {
		t.Fatal("the delivered message lost the series' verbatim iCalendar")
	}
	if raw, _ := v.([]byte); !bytes.Equal(raw, body) {
		t.Errorf("verbatim iCalendar = %q, want the bytes the invitation carried", raw)
	}
}
