package oxcical

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestOrganizerIdentityCannotInjectLines is the iCal injection defect. The
// ORGANIZER and ATTENDEE lines carry parameters and a URI, not a TEXT value, so
// they are written unescaped. A line break in the stored display name or address
// therefore ended the line and started a property of the client's choosing, and
// the export is served over CalDAV and embedded in relayed invites, so the
// injected property reaches other clients.
func TestOrganizerIdentityCannotInjectLines(t *testing.T) {
	r := newResolver()
	_, _ = r.resolve(true, []mapi.PropertyName{mapi.NameAppointmentStartWhole, nameICalUID})
	msg := &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Schedule.Meeting.Resp.Neg"},
		{Tag: r.tag(mapi.NameAppointmentStartWhole, mapi.PtSysTime), Value: mapi.UnixToNTTime(time.Date(2026, 7, 1, 14, 0, 0, 0, time.UTC))},
		{Tag: r.tag(nameICalUID, mapi.PtUnicode), Value: "meeting-42"},
		{Tag: mapi.PrSubject, Value: "Quarterly Review"},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "organizer@hermex.test"},
		{Tag: mapi.PrSentRepresentingName, Value: "Boss\r\nX-INJECTED:organizer"},
		{Tag: mapi.PrSenderSmtpAddress, Value: "alice@hermex.test\r\nX-INJECTED:attendee"},
		{Tag: mapi.PrSenderName, Value: "Alice"},
	}}

	out, err := Export(msg, r.opt())
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(out), "\r\n") {
		if strings.HasPrefix(line, "X-INJECTED") {
			t.Errorf("an identity line break started a property of its own:\n%s", out)
		}
	}
}

// TestOrdinaryIdentitySurvives is the control: cleaning must not damage an
// ordinary organizer or attendee line.
func TestOrdinaryIdentitySurvives(t *testing.T) {
	r := newResolver()
	_, _ = r.resolve(true, []mapi.PropertyName{mapi.NameAppointmentStartWhole, nameICalUID})
	msg := &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Schedule.Meeting.Resp.Neg"},
		{Tag: r.tag(mapi.NameAppointmentStartWhole, mapi.PtSysTime), Value: mapi.UnixToNTTime(time.Date(2026, 7, 1, 14, 0, 0, 0, time.UTC))},
		{Tag: r.tag(nameICalUID, mapi.PtUnicode), Value: "meeting-42"},
		{Tag: mapi.PrSubject, Value: "Quarterly Review"},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "organizer@hermex.test"},
		{Tag: mapi.PrSentRepresentingName, Value: "The Organizer"},
		{Tag: mapi.PrSenderSmtpAddress, Value: "alice@hermex.test"},
		{Tag: mapi.PrSenderName, Value: "Alice"},
	}}

	out, err := Export(msg, r.opt())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		`ORGANIZER;CN="The Organizer":mailto:organizer@hermex.test`,
		`ATTENDEE;PARTSTAT=DECLINED;CN="Alice":mailto:alice@hermex.test`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("ordinary export missing %q\n%s", want, s)
		}
	}
}
