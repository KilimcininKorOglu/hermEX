package webmail2api

import (
	"net/http"
	"testing"
)

// TestEventBusyStatusRoundTripsWorkingElsewhere pins PidLidBusyStatus 4. An
// appointment migrated from Exchange commonly carries it, and iCalendar cannot
// express it (TRANSP only says opaque or transparent), so it survives only because
// the value is read and written as the named property directly. A value past the
// five [MS-OXOCAL] 2.2.1.2 defines is refused and stores no event.
func TestEventBusyStatusRoundTripsWorkingElsewhere(t *testing.T) {
	do := apiDo(t)

	wantStatus(t, "create with busy status 5", do(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Bogus","start":"2026-08-15T09:00:00Z","end":"2026-08-15T10:00:00Z","busyStatus":5}`), http.StatusBadRequest)
	wantEq(t, "events after the refused create", len(listEvents(t, do, "list after refusal")), 0)

	wantStatus(t, "create", do(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Offsite","start":"2026-08-15T09:00:00Z","end":"2026-08-15T10:00:00Z","busyStatus":4}`), http.StatusOK)
	events := listEvents(t, do, "list")
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	got := events[0].BusyStatus
	if got == nil || *got != 4 {
		t.Errorf("busyStatus = %v, want 4 (working elsewhere)", got)
	}
}
