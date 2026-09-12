package dav

import (
	"testing"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// zoneCalBody is one event whose times carry the given TZID parameter.
func zoneCalBody(tzidParam string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:zone-put-1\r\n" +
		"DTSTAMP:20260101T090000Z\r\nSUMMARY:Zone\r\n" +
		"DTSTART" + tzidParam + ":20260811T103000\r\nDTEND" + tzidParam + ":20260811T113000\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n")
}

// zonePut runs a calendar PUT body through the import path and returns the events
// it logged.
func zonePut(t *testing.T, body []byte) []logging.Event {
	t.Helper()
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	srv := NewServer(nil, nil, "hermex.test")
	sink := &scheduleSink{}
	srv.Logger = logging.New(sink)

	if _, _, err := srv.importCalendarBody(st, int64(mapi.PrivateFIDCalendar), body); err != nil {
		t.Fatalf("import: %v", err)
	}
	return sink.events
}

// zoneEvent returns the zone-loss event among the logged ones, or nil.
func zoneEvent(events []logging.Event) *logging.Event {
	for i := range events {
		if events[i].Name == "calendar.zone_unresolved" {
			return &events[i]
		}
	}
	return nil
}

// TestCalendarPutLogsAnUnresolvableZone proves the operator is told when a PUT's
// times are stored as UTC for want of a zone. The write is accepted, so the only
// other evidence would be the wrong hour itself, and the zone id (the one fact
// needed to extend the zone table) would be lost with the request body.
func TestCalendarPutLogsAnUnresolvableZone(t *testing.T) {
	ev := zoneEvent(zonePut(t, zoneCalBody(";TZID=Mars/Olympus")))
	if ev == nil {
		t.Fatal("an unresolvable TZID was not logged")
	}
	if ev.Level != logging.LevelWarn {
		t.Errorf("level = %v, want warn", ev.Level)
	}
	zones, ok := ev.Fields["zones"].([]string)
	if !ok || len(zones) != 1 || zones[0] != "Mars/Olympus" {
		t.Errorf("zones field = %#v, want the zone id", ev.Fields["zones"])
	}
	if got, ok := ev.Fields["times"].(int); !ok || got != 2 {
		t.Errorf("times field = %#v, want 2 (DTSTART and DTEND)", ev.Fields["times"])
	}
}

// TestCalendarPutStaysSilentForAResolvedZone is the negative control: a Windows
// zone id now resolves, so the event above means what it says.
func TestCalendarPutStaysSilentForAResolvedZone(t *testing.T) {
	if ev := zoneEvent(zonePut(t, zoneCalBody(";TZID=W. Europe Standard Time"))); ev != nil {
		t.Fatalf("a resolved zone was logged as a loss: %+v", ev)
	}
}
