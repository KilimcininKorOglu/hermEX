package dav

import (
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// scheduleSink collects the events an implicit-scheduling pass emits.
type scheduleSink struct{ events []logging.Event }

func (s *scheduleSink) Write(e logging.Event) { s.events = append(s.events, e) }

// inviteBody is an organizer-side event inviting one attendee who has no mailbox
// here, so the delivery pass reports that address as unresolved.
const inviteBody = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\n" +
	"UID:implicit-log-1\r\nDTSTAMP:20260101T090000Z\r\nDTSTART:20260201T090000Z\r\n" +
	"SUMMARY:Board review\r\nORGANIZER:mailto:alice@hermex.test\r\n" +
	"ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:secret.attendee@example.com\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// TestUnresolvedSchedulingLogsACountNotAddresses proves an undeliverable iTIP
// message records how many recipients failed, never who they were. The recipients
// are a meeting's attendee list, and every event here goes to the shared log sink
// that any operator with panel access can browse, so an attendee list must not
// travel with it.
func TestUnresolvedSchedulingLogsACountNotAddresses(t *testing.T) {
	accs := directory.StaticAccounts{
		"alice@hermex.test": {Password: "pw", MailboxPath: t.TempDir()},
	}
	srv := NewServer(accs, accs, "hermex.test")
	sink := &scheduleSink{}
	srv.Logger = logging.New(sink)

	srv.scheduleOnChange("alice@hermex.test", "", inviteBody, false)

	var ev *logging.Event
	for i := range sink.events {
		if sink.events[i].Name == "schedule.deliver.unresolved" {
			ev = &sink.events[i]
		}
	}
	if ev == nil {
		t.Fatalf("no schedule.deliver.unresolved event; got %+v", sink.events)
	}
	if got, ok := ev.Fields["unresolved"].(int); !ok || got != 1 {
		t.Errorf("unresolved field = %#v, want the count 1", ev.Fields["unresolved"])
	}
	for key, val := range ev.Fields {
		if s, ok := val.(string); ok && strings.Contains(s, "@") {
			t.Errorf("field %q carries an address: %q", key, s)
		}
	}
	if strings.Contains(strings.ToLower(ev.User), "secret.attendee") {
		t.Errorf("the attendee leaked through the User field: %q", ev.User)
	}
}

// TestSchedulingStaysSilentWhenEverythingDelivers is the negative control: a
// scheduling pass whose recipients all resolve must emit nothing, so the event
// above means what it says.
func TestSchedulingStaysSilentWhenEverythingDelivers(t *testing.T) {
	accs := directory.StaticAccounts{
		"alice@hermex.test": {Password: "pw", MailboxPath: t.TempDir()},
		"bob@hermex.test":   {Password: "pw", MailboxPath: t.TempDir()},
	}
	srv := NewServer(accs, accs, "hermex.test")
	sink := &scheduleSink{}
	srv.Logger = logging.New(sink)

	local := strings.Replace(inviteBody, "secret.attendee@example.com", "bob@hermex.test", 1)
	srv.scheduleOnChange("alice@hermex.test", "", local, false)

	for _, e := range sink.events {
		if strings.HasPrefix(e.Name, "schedule.") {
			t.Errorf("a fully delivered invite still logged %s: %+v", e.Name, e.Fields)
		}
	}
}
