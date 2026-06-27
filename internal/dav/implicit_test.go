package dav

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// schedEvent builds a VCALENDAR/VEVENT body for broker tests.
func schedEvent(uid, organizer, summary, dtstart string, seq int, attendees ...string) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\n")
	fmt.Fprintf(&b, "UID:%s\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:%s\r\nSEQUENCE:%d\r\n", uid, dtstart, seq)
	if summary != "" {
		fmt.Fprintf(&b, "SUMMARY:%s\r\n", summary)
	}
	fmt.Fprintf(&b, "ORGANIZER:mailto:%s\r\n", organizer)
	for _, a := range attendees {
		fmt.Fprintf(&b, "ATTENDEE:mailto:%s\r\n", a)
	}
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String()
}

// msgsTo returns the first scheduling message with the given method.
func msgsTo(msgs []itipMsg, method string) (itipMsg, bool) {
	for _, m := range msgs {
		if m.method == method {
			return m, true
		}
	}
	return itipMsg{}, false
}

// recipientsHave reports whether any recipient resolves to addr.
func recipientsHave(recipients []string, addr string) bool {
	for _, r := range recipients {
		if normalizeCalAddr(r) == addr {
			return true
		}
	}
	return false
}

// TestSchedulingBrokerOrganizer exercises the organizer-side broker: invites on
// create, the significant-change resend guard, added/removed attendees, deletion, and
// the SCHEDULE-AGENT=CLIENT opt-out (RFC 6638 §3 / RFC 5546 §2.1.4).
func TestSchedulingBrokerOrganizer(t *testing.T) {
	const (
		alice = "alice@hermex.test"
		bob   = "bob@hermex.test"
		carol = "carol@hermex.test"
	)
	mtg := func(summary, dtstart string, seq int, atts ...string) string {
		return schedEvent("m-1", alice, summary, dtstart, seq, atts...)
	}

	t.Run("create invites every attendee", func(t *testing.T) {
		msgs := schedulingMessages(alice, "", mtg("Plan", "20260701T140000Z", 0, bob, carol))
		req, ok := msgsTo(msgs, "REQUEST")
		if !ok {
			t.Fatalf("create produced no REQUEST: %+v", msgs)
		}
		if !recipientsHave(req.recipients, bob) || !recipientsHave(req.recipients, carol) {
			t.Errorf("REQUEST recipients %v, want bob+carol", req.recipients)
		}
		if !strings.Contains(req.body, "METHOD:REQUEST") {
			t.Errorf("REQUEST body lacks METHOD:REQUEST")
		}
	})

	t.Run("unchanged re-put sends nothing", func(t *testing.T) {
		body := mtg("Plan", "20260701T140000Z", 0, bob)
		if msgs := schedulingMessages(alice, body, body); len(msgs) != 0 {
			t.Errorf("unchanged re-PUT produced %d messages, want 0: %+v", len(msgs), msgs)
		}
	})

	t.Run("time change re-invites retained attendee", func(t *testing.T) {
		old := mtg("Plan", "20260701T140000Z", 0, bob)
		neu := mtg("Plan", "20260701T160000Z", 1, bob)
		req, ok := msgsTo(schedulingMessages(alice, old, neu), "REQUEST")
		if !ok || !recipientsHave(req.recipients, bob) {
			t.Errorf("time change did not re-invite bob")
		}
	})

	t.Run("summary-only change skips retained attendee", func(t *testing.T) {
		old := mtg("Plan", "20260701T140000Z", 0, bob)
		neu := mtg("Plan v2", "20260701T140000Z", 0, bob)
		if msgs := schedulingMessages(alice, old, neu); len(msgs) != 0 {
			t.Errorf("insignificant change produced %d messages, want 0: %+v", len(msgs), msgs)
		}
	})

	t.Run("added attendee invited, retained not", func(t *testing.T) {
		old := mtg("Plan", "20260701T140000Z", 0, bob)
		neu := mtg("Plan", "20260701T140000Z", 0, bob, carol)
		req, ok := msgsTo(schedulingMessages(alice, old, neu), "REQUEST")
		if !ok || !recipientsHave(req.recipients, carol) {
			t.Fatalf("added attendee carol not invited")
		}
		if recipientsHave(req.recipients, bob) {
			t.Errorf("retained attendee bob re-invited on an insignificant change")
		}
	})

	t.Run("removed attendee cancelled", func(t *testing.T) {
		old := mtg("Plan", "20260701T140000Z", 0, bob, carol)
		neu := mtg("Plan", "20260701T140000Z", 0, bob)
		cancel, ok := msgsTo(schedulingMessages(alice, old, neu), "CANCEL")
		if !ok || !recipientsHave(cancel.recipients, carol) {
			t.Errorf("removed attendee carol not cancelled")
		}
		if recipientsHave(cancel.recipients, bob) {
			t.Errorf("retained attendee bob wrongly cancelled")
		}
	})

	t.Run("delete cancels all attendees", func(t *testing.T) {
		old := mtg("Plan", "20260701T140000Z", 0, bob, carol)
		cancel, ok := msgsTo(schedulingMessages(alice, old, ""), "CANCEL")
		if !ok || !recipientsHave(cancel.recipients, bob) || !recipientsHave(cancel.recipients, carol) {
			t.Errorf("delete did not cancel all attendees: %+v", cancel)
		}
		if !strings.Contains(cancel.body, "STATUS:CANCELLED") {
			t.Errorf("CANCEL body lacks STATUS:CANCELLED")
		}
	})

	t.Run("schedule-agent client excluded", func(t *testing.T) {
		body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:m-1\r\nDTSTART:20260701T140000Z\r\n" +
			"ORGANIZER:mailto:" + alice + "\r\nATTENDEE;SCHEDULE-AGENT=CLIENT:mailto:" + bob + "\r\n" +
			"END:VEVENT\r\nEND:VCALENDAR\r\n"
		if msgs := schedulingMessages(alice, "", body); len(msgs) != 0 {
			t.Errorf("SCHEDULE-AGENT=CLIENT attendee was scheduled: %+v", msgs)
		}
	})
}

// TestSchedulingBrokerAttendeeReply confirms an attendee's PARTSTAT change produces a
// REPLY to the organizer, and an unchanged PARTSTAT produces nothing (RFC 6638 §3.2.2).
func TestSchedulingBrokerAttendeeReply(t *testing.T) {
	const (
		alice = "alice@hermex.test" // organizer
		bob   = "bob@hermex.test"   // owner / attendee
	)
	ev := func(partstat string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:m-1\r\nDTSTART:20260701T140000Z\r\nSEQUENCE:0\r\n" +
			"ORGANIZER:mailto:" + alice + "\r\nATTENDEE;PARTSTAT=" + partstat + ":mailto:" + bob + "\r\n" +
			"END:VEVENT\r\nEND:VCALENDAR\r\n"
	}
	reply, ok := msgsTo(schedulingMessages(bob, ev("NEEDS-ACTION"), ev("ACCEPTED")), "REPLY")
	if !ok {
		t.Fatal("partstat change produced no REPLY")
	}
	if !recipientsHave(reply.recipients, alice) {
		t.Errorf("REPLY not addressed to the organizer: %v", reply.recipients)
	}
	if !strings.Contains(reply.body, "PARTSTAT=ACCEPTED") || !strings.Contains(reply.body, "METHOD:REPLY") {
		t.Errorf("REPLY body wrong:\n%s", reply.body)
	}
	if m := schedulingMessages(bob, ev("ACCEPTED"), ev("ACCEPTED")); len(m) != 0 {
		t.Errorf("unchanged partstat produced %d messages, want 0", len(m))
	}
}

// TestOptionsAutoSchedule confirms OPTIONS advertises the calendar-auto-schedule
// class, the signal that tells a client to let the server deliver iTIP on PUT rather
// than POSTing it to the Outbox (which would double-send).
func TestOptionsAutoSchedule(t *testing.T) {
	ts := davServerCal(t)
	resp, _ := do(t, ts, "OPTIONS", "/dav/calendars/"+testUser+"/calendar/", "", true)
	if dav := resp.Header.Get("DAV"); !strings.Contains(dav, "calendar-auto-schedule") {
		t.Errorf("DAV header %q lacks calendar-auto-schedule", dav)
	}
}

// TestImplicitSchedulePutDelivers confirms an organizer's PUT of a meeting auto-sends
// exactly one REQUEST to a local attendee (RFC 6638 §3), and a re-PUT of the identical
// event does not re-invite, proving the significant-change resend guard holds across
// the MAPI round-trip (which now preserves the attendee set).
func TestImplicitSchedulePutDelivers(t *testing.T) {
	ts, bobDir := davServerWithPeer(t)
	body := schedEvent("imp-1", testUser, "Sprint", "20260701T140000Z", 0, "bob@hermex.test")

	resp, out := doFull(t, ts, "PUT", "/dav/calendars/"+testUser+"/calendar/imp-1.ics", body, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d, want 201\n%s", resp.StatusCode, out)
	}
	n, class, subject := inboxMessage(t, bobDir)
	if n != 1 {
		t.Fatalf("bob's inbox has %d messages, want exactly 1", n)
	}
	if !strings.Contains(class, "Schedule.Meeting.Request") {
		t.Errorf("delivered class %q, want a meeting request", class)
	}
	if subject != "Sprint" {
		t.Errorf("delivered subject %q, want Sprint", subject)
	}

	resp2, out2 := doFull(t, ts, "PUT", "/dav/calendars/"+testUser+"/calendar/imp-1.ics", body, nil)
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("re-PUT status %d, want 204\n%s", resp2.StatusCode, out2)
	}
	if n2, _, _ := inboxMessage(t, bobDir); n2 != 1 {
		t.Fatalf("re-PUT re-delivered: bob's inbox has %d, want still 1", n2)
	}
}
