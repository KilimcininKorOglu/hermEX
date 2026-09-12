package objectstore

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
)

// inviteWithZone is a delivered meeting request whose appointment times carry the
// given TZID parameter, the shape Outlook and Exchange send.
func inviteWithZone(tzidParam string) []byte {
	ical := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\n" +
		"UID:zone-invite-1\r\nDTSTAMP:20260101T090000Z\r\nSUMMARY:Kickoff\r\n" +
		"ORGANIZER:mailto:bob@hermex.test\r\nATTENDEE:mailto:alice@hermex.test\r\n" +
		"DTSTART" + tzidParam + ":20260811T103000\r\nDTEND" + tzidParam + ":20260811T113000\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	return []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\n" +
		"Subject: Kickoff\r\nDate: Thu, 01 Jan 2026 09:00:00 +0000\r\n" +
		"Message-ID: <zone-invite-1@hermex.test>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n\r\n" + ical)
}

// deliverInvite appends a meeting request to the Inbox and returns the stored
// message plus the events delivery logged.
func deliverInvite(t *testing.T, raw []byte) (*Store, MessageInfo, *captureSink) {
	t.Helper()
	return deliverInviteInZone(t, raw, nil)
}

// deliverInviteInZone is deliverInvite with the mailbox owner's zone set, the way
// the delivery path sets it from the directory record.
func deliverInviteInZone(t *testing.T, raw []byte, zone *time.Location) (*Store, MessageInfo, *captureSink) {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	sink := &captureSink{}
	st.logger = logging.New(sink)
	st.SetDefaultZone(zone)

	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	return st, info, sink
}

// appointmentStart reads the stored appointment's start instant.
func appointmentStart(t *testing.T, st *Store, id int64) time.Time {
	t.Helper()
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameAppointmentStartWhole})
	if err != nil {
		t.Fatalf("named ids: %v", err)
	}
	if ids[0] == 0 {
		t.Fatal("the delivered message stored no appointment start")
	}
	tag := mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtSysTime))
	props, err := st.GetMessageProperties(id, tag)
	if err != nil {
		t.Fatalf("get props: %v", err)
	}
	v, ok := props.Get(tag)
	if !ok {
		t.Fatal("the delivered message stored no appointment start")
	}
	nt, ok := v.(uint64)
	if !ok {
		t.Fatalf("start is %T, want uint64", v)
	}
	return mapi.NTTimeToUnix(nt).UTC()
}

// TestDeliveredInviteResolvesAWindowsZone is the defect this change fixes on the
// path that carries almost every external invitation: Outlook and Exchange write
// the Windows zone id in TZID, nothing resolved it, and the meeting was stored an
// offset away from the hour the organizer picked. Berlin is UTC+2 in August, so
// 10:30 local is 08:30Z.
func TestDeliveredInviteResolvesAWindowsZone(t *testing.T) {
	st, info, _ := deliverInvite(t, inviteWithZone(";TZID=W. Europe Standard Time"))
	got := appointmentStart(t, st, info.ID)
	want := time.Date(2026, 8, 11, 8, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("stored start = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestDeliveredInviteLogsAnUnresolvableZone proves the residual is recorded. A
// zone nothing resolves still lands as UTC, and the zone id is the one fact
// needed to extend the table; without this line it is lost with the message.
func TestDeliveredInviteLogsAnUnresolvableZone(t *testing.T) {
	_, _, sink := deliverInvite(t, inviteWithZone(";TZID=Mars/Olympus"))
	e, ok := sink.find("calendar.zone_unresolved")
	if !ok {
		t.Fatal("an unresolvable TZID was not logged")
	}
	if e.Level != logging.LevelWarn {
		t.Errorf("level = %v, want warn", e.Level)
	}
	zones, ok := e.Fields["zones"].([]string)
	if !ok || len(zones) != 1 || zones[0] != "Mars/Olympus" {
		t.Errorf("zones field = %#v, want the zone id", e.Fields["zones"])
	}
	if got, ok := e.Fields["mailbox"].(string); !ok || got == "" {
		t.Errorf("mailbox field = %#v, want the store directory", e.Fields["mailbox"])
	}
	wantNoAddresses(t, e.Fields)
}

// wantNoAddresses fails when a logged field carries a mail address. Every event
// here goes to the shared log sink any operator with panel access can browse, so
// the participants of a meeting must not travel with it.
func wantNoAddresses(t *testing.T, fields logging.Fields) {
	t.Helper()
	for key, val := range fields {
		if s, ok := val.(string); ok && strings.Contains(s, "@") {
			t.Errorf("field %q carries an address: %q", key, s)
		}
	}
}

// TestDeliveredFloatingInviteUsesTheMailboxZone covers an invitation whose times
// carry no zone at all. Such a value means the reader's own wall clock, so it
// belongs in the mailbox owner's zone; stored without one it lands wrong by that
// owner's offset. Istanbul is UTC+3, so 10:30 there is 07:30Z.
func TestDeliveredFloatingInviteUsesTheMailboxZone(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	st, info, sink := deliverInviteInZone(t, inviteWithZone(""), zone)
	got := appointmentStart(t, st, info.ID)
	want := time.Date(2026, 8, 11, 7, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("stored start = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if _, ok := sink.find("calendar.zone_unresolved"); ok {
		t.Error("a floating time read in the mailbox zone was logged as a loss")
	}
}

// TestDeliveredFloatingInviteIsLoggedWithoutAMailboxZone is the other side: with
// no zone for the mailbox the time is still read as UTC, and that is recorded
// rather than passing silently.
func TestDeliveredFloatingInviteIsLoggedWithoutAMailboxZone(t *testing.T) {
	_, _, sink := deliverInvite(t, inviteWithZone(""))
	e, ok := sink.find("calendar.zone_unresolved")
	if !ok {
		t.Fatal("a floating time read as UTC was not logged")
	}
	zones, _ := e.Fields["zones"].([]string)
	if len(zones) != 1 || zones[0] != "(floating)" {
		t.Errorf("zones field = %#v, want the floating marker", e.Fields["zones"])
	}
}

// TestDeliveredInviteStaysSilentForAResolvedZone is the negative control: a zone
// that resolves is not a loss and must not be logged.
func TestDeliveredInviteStaysSilentForAResolvedZone(t *testing.T) {
	_, _, sink := deliverInvite(t, inviteWithZone(";TZID=Europe/Istanbul"))
	if _, ok := sink.find("calendar.zone_unresolved"); ok {
		t.Fatal("a resolved zone was logged as a loss")
	}
}
