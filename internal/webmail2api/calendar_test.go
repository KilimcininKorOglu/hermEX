package webmail2api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestCalendarEventUIDIsMessageID proves a listed event is identified by its store
// message id, not its iCalendar UID. The delete and update handlers parse the uid
// back to a store id, so a created-then-reloaded event must carry the numeric id;
// surfacing the string iCalendar UID (the meeting identity) would make delete and
// update fail to parse it, leaving the event uneditable after a refresh.
func TestCalendarEventUIDIsMessageID(t *testing.T) {
	do, _ := apiHarness(t)

	// Create an event with no client-supplied uid (the SPA's normal path, which
	// makes the server mint an iCalendar UID).
	wantStatus(t, "create", do(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Standup","start":"2026-08-02T09:00:00Z"}`), http.StatusOK)

	// Read it back the way the SPA does after a reload.
	uid := listOneEvent(t, do, "list").UID
	if _, err := strconv.ParseInt(uid, 10, 64); err != nil {
		t.Fatalf("listed event uid %q is not a numeric message id; delete/update would fail to parse it", uid)
	}

	// The delete handler parses that uid back to a store id; it must succeed (a
	// string iCalendar uid would 400 here).
	wantStatus(t, "delete by listed uid", do(http.MethodDelete, "/api/v1/calendar/events/"+uid, ""), http.StatusOK)

	// The event is gone.
	wantEq(t, "events after delete", len(listEvents(t, do, "list after delete")), 0)
}

// listEvents reads the calendar listing the SPA reloads.
func listEvents(t *testing.T, do requestFunc, what string) []eventJSON {
	t.Helper()
	type listing struct {
		Events []eventJSON `json:"events"`
	}
	return okBody[listing](t, what, do(http.MethodGet, "/api/v1/calendar/events", "")).Events
}

// listOneEvent reads the listing and requires it to hold exactly one event.
func listOneEvent(t *testing.T, do requestFunc, what string) eventJSON {
	t.Helper()
	events := listEvents(t, do, what)
	if len(events) != 1 {
		t.Fatalf("%s: got %d events, want 1", what, len(events))
	}
	return events[0]
}

// TestCalendarEventReminderRoundTrip proves a reminder set in the SPA form
// survives a create-then-reload: the reminder lead time round-trips through
// oxcical's VALARM (NameReminderSet/NameReminderDelta named props) and comes
// back as the same reminderMinutes on the listed event. A silent loss here would
// make the form's reminder select a no-op after a refresh, so the assertion is on
// the exact minute value, not just presence.
func TestCalendarEventReminderRoundTrip(t *testing.T) {
	do, _ := apiHarness(t)

	// Create a timed event with a 15-minute reminder.
	wantStatus(t, "create with reminder", do(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Standup","start":"2026-08-02T09:00:00Z","reminderMinutes":15}`), http.StatusOK)

	// Reload it the way the SPA does; the reminder must come back as 15.
	listed := listOneEvent(t, do, "list")
	if listed.ReminderMinutes == nil {
		t.Fatal("listed event has no reminderMinutes, want 15 (VALARM round-trip lost it)")
	}
	wantEq(t, "reminderMinutes", *listed.ReminderMinutes, 15)

	// Clear the reminder by updating without one; the reloaded event must have none.
	wantStatus(t, "update clearing reminder", do(http.MethodPut, "/api/v1/calendar/events/"+listed.UID,
		`{"summary":"Standup","start":"2026-08-02T09:00:00Z"}`), http.StatusOK)
	after := listOneEvent(t, do, "list after update")
	if after.ReminderMinutes != nil {
		t.Fatalf("reminderMinutes = %d after update without reminder, want nil", *after.ReminderMinutes)
	}
}

// TestCalendarSettingsRoundTrip proves the week-start setting persists in the
// shared webmail settings blob (DB-backed, per-user), not a client-side shortcut:
// a PUT then a fresh GET (the SPA's reload path) must return the stored weekday.
// A silent loss here would regress to a client-only store that does not survive a
// reload or apply cross-device, the shortcut the user rejected.
func TestCalendarSettingsRoundTrip(t *testing.T) {
	do, _ := apiHarness(t)
	readSettings := func(what string) calendarSettingsJSON {
		t.Helper()
		return okBody[calendarSettingsJSON](t, what, do(http.MethodGet, "/api/v1/calendar/settings", ""))
	}

	// A fresh account defaults to Monday (1) before any setting is stored.
	wantEq(t, "default firstDayOfWeek (Monday)", readSettings("get default").FirstDayOfWeek, 1)

	// Persist Sunday (0) and read it back the way the SPA does after a reload.
	wantStatus(t, "put", do(http.MethodPut, "/api/v1/calendar/settings", `{"firstDayOfWeek":0}`), http.StatusOK)
	wantEq(t, "firstDayOfWeek after put (Sunday, persisted in the settings blob)",
		readSettings("get after put").FirstDayOfWeek, 0)

	// An out-of-range value is clamped to the Monday default, never stored as-is.
	wantStatus(t, "put invalid", do(http.MethodPut, "/api/v1/calendar/settings", `{"firstDayOfWeek":9}`), http.StatusOK)
	wantEq(t, "firstDayOfWeek after an invalid put (clamped to Monday)",
		readSettings("get after invalid").FirstDayOfWeek, 1)
}

// TestCalendarEventBusyStatusRoundTrip proves the busy-status (free/tentative/busy/
// oof) round-trips through the direct named-prop path, including oof which the iCal
// TRANSP/STATUS path oxcical uses cannot express. A silent loss here would make the
// form's "Show as" select drop oof back to busy after a reload.
func TestCalendarEventBusyStatusRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	do := apiHarnessFor(t, dir)

	for _, want := range []int{0, 1, 2, 3} {
		// Create an event with this busy status, then reload and assert it survived.
		wantStatus(t, "create busyStatus", do(http.MethodPost, "/api/v1/calendar/events",
			`{"summary":"Meeting","start":"2026-08-02T09:00:00Z","busyStatus":`+strconv.Itoa(want)+`}`), http.StatusOK)
		listed := listOneEvent(t, do, "list")
		if listed.BusyStatus == nil {
			t.Fatalf("busyStatus=%d: listed event has no busyStatus (round-trip lost it)", want)
		}
		wantEq(t, "busyStatus (oof would be lost via the iCal path)", *listed.BusyStatus, want)
		// Clear it for the next iteration so only one event is present.
		deleteEvent(t, do, listed.UID)
	}
}

// deleteEvent removes one event by the uid the listing reported.
func deleteEvent(t *testing.T, do requestFunc, uid string) {
	t.Helper()
	wantStatus(t, "delete", do(http.MethodDelete, "/api/v1/calendar/events/"+uid, ""), http.StatusOK)
}

// TestCalendarEventSensitivityRoundTrip proves the sensitivity (normal/private/
// confidential) round-trips through the iCal CLASS property. Private and
// confidential must survive a reload; normal stays unset (absent) so the form
// shows the default.
func TestCalendarEventSensitivityRoundTrip(t *testing.T) {
	do, _ := apiHarness(t)

	for _, want := range []int{2, 3} {
		wantStatus(t, "create sensitivity", do(http.MethodPost, "/api/v1/calendar/events",
			`{"summary":"Meeting","start":"2026-08-02T09:00:00Z","sensitivity":`+strconv.Itoa(want)+`}`), http.StatusOK)
		listed := listOneEvent(t, do, "list")
		if listed.Sensitivity == nil {
			t.Fatalf("sensitivity=%d: listed event has no sensitivity (CLASS round-trip lost it)", want)
		}
		wantEq(t, "sensitivity", *listed.Sensitivity, want)
		deleteEvent(t, do, listed.UID)
	}

	// A normal (sensitivity unset) event must not surface a sensitivity after reload.
	wantStatus(t, "create normal", do(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Open","start":"2026-08-02T09:00:00Z"}`), http.StatusOK)
	if s := listOneEvent(t, do, "list normal").Sensitivity; s != nil {
		t.Fatalf("normal event has sensitivity = %d, want nil", *s)
	}
}

// TestCalendarEventCategoriesRoundTrip proves the category list (PidNameKeywords,
// the shared cross-protocol list) round-trips through the direct store accessor.
// A silent loss here would drop the event's categories after a reload.
func TestCalendarEventCategoriesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("calendar-categories-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"Meeting","start":"2026-08-02T09:00:00Z","categories":["Work","Urgent"]}`); rec.Code != http.StatusOK {
		t.Fatalf("create: status %d", rec.Code)
	}
	rec := do(http.MethodGet, "/api/v1/calendar/events", "")
	var listed struct {
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(listed.Events))
	}
	got := listed.Events[0].Categories
	if len(got) != 2 || got[0] != "Work" || got[1] != "Urgent" {
		t.Fatalf("categories = %v, want [Work Urgent]", got)
	}
}

// TestGetEventsRespectsWindow proves the events list honors an optional [start,end)
// query window: an appointment outside the window is pruned before export, while a
// request without a window still returns the whole calendar. This bounds the
// backend iCal-export cost to the visible range instead of the account's age.
func TestGetEventsRespectsWindow(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("calendar-window-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	list := func(target string) []eventJSON {
		rec := do(http.MethodGet, target, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list %q: status %d", target, rec.Code)
		}
		var out struct {
			Events []eventJSON `json:"events"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %q: %v", target, err)
		}
		return out.Events
	}

	// One appointment inside the window, one far outside it.
	if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"InWindow","start":"2026-08-15T09:00:00Z","end":"2026-08-15T10:00:00Z"}`); rec.Code != http.StatusOK {
		t.Fatalf("create in-window: status %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"OutOfWindow","start":"2020-01-15T09:00:00Z","end":"2020-01-15T10:00:00Z"}`); rec.Code != http.StatusOK {
		t.Fatalf("create out-of-window: status %d", rec.Code)
	}

	// No window: the whole calendar is returned (backward compatible).
	if all := list("/api/v1/calendar/events"); len(all) != 2 {
		t.Fatalf("unwindowed list = %d events, want 2", len(all))
	}

	// Windowed to August 2026: only the in-window appointment survives.
	scoped := list("/api/v1/calendar/events?start=2026-08-01T00:00:00Z&end=2026-09-01T00:00:00Z")
	if len(scoped) != 1 {
		t.Fatalf("windowed list = %d events, want 1", len(scoped))
	}
	if scoped[0].Summary != "InWindow" {
		t.Fatalf("windowed event = %q, want InWindow", scoped[0].Summary)
	}
}

// TestMeetingRequestRejectsHeaderInjection proves a CR/LF in the event summary
// cannot inject an extra RFC 5322 header (or terminate the header block) into an
// outbound meeting request, and cannot break out of the iCalendar SUMMARY line.
// Without sanitizing the summary, an authenticated user could add a Reply-To or
// Bcc header to a DKIM-signed invite relayed to attendees.
func TestMeetingRequestRejectsHeaderInjection(t *testing.T) {
	for _, build := range []struct {
		name string
		fn   func(string, eventJSON) ([]byte, []string, error)
	}{
		{"request", buildMeetingRequest},
		{"cancel", buildCancellationRequest},
	} {
		raw, _, err := build.fn("organizer@hermex.test", eventJSON{
			Summary:   "Sync\r\nReply-To: attacker@evil.example\r\nBcc: leak@evil.example",
			Attendees: []string{"victim@ext.example"},
			Start:     "2026-09-01T10:00:00Z",
			End:       "2026-09-01T11:00:00Z",
		})
		if err != nil {
			t.Fatalf("%s: build: %v", build.name, err)
		}
		header, _, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
		for line := range bytes.SplitSeq(header, []byte("\r\n")) {
			low := bytes.ToLower(line)
			if bytes.HasPrefix(low, []byte("reply-to:")) || bytes.HasPrefix(low, []byte("bcc:")) {
				t.Errorf("%s: injected header line survived into the header block: %q", build.name, line)
			}
		}
		// The iCalendar SUMMARY must not contain a raw CRLF that starts a new
		// property; the escaper turns the newline into a literal \n on one line.
		if bytes.Contains(raw, []byte("SUMMARY:Sync\r\nReply-To")) {
			t.Errorf("%s: raw CRLF broke out of the iCalendar SUMMARY line", build.name)
		}
	}
}
