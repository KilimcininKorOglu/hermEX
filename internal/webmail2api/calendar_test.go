package webmail2api

import (
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
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("calendar-uid-test-secret")
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

	// Create an event with no client-supplied uid (the SPA's normal path, which
	// makes the server mint an iCalendar UID).
	if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"Standup","start":"2026-08-02T09:00:00Z"}`); rec.Code != http.StatusOK {
		t.Fatalf("create: status %d", rec.Code)
	}

	// Read it back the way the SPA does after a reload.
	rec := do(http.MethodGet, "/api/v1/calendar/events", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d", rec.Code)
	}
	var listed struct {
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(listed.Events))
	}
	uid := listed.Events[0].UID
	if _, err := strconv.ParseInt(uid, 10, 64); err != nil {
		t.Fatalf("listed event uid %q is not a numeric message id; delete/update would fail to parse it", uid)
	}

	// The delete handler parses that uid back to a store id; it must succeed (a
	// string iCalendar uid would 400 here).
	if rec := do(http.MethodDelete, "/api/v1/calendar/events/"+uid, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete by listed uid: status %d", rec.Code)
	}

	// The event is gone.
	rec = do(http.MethodGet, "/api/v1/calendar/events", "")
	var after struct {
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode list after delete: %v", err)
	}
	if len(after.Events) != 0 {
		t.Fatalf("event still present after delete: %d", len(after.Events))
	}
}

// TestCalendarEventReminderRoundTrip proves a reminder set in the SPA form
// survives a create-then-reload: the reminder lead time round-trips through
// oxcical's VALARM (NameReminderSet/NameReminderDelta named props) and comes
// back as the same reminderMinutes on the listed event. A silent loss here would
// make the form's reminder select a no-op after a refresh, so the assertion is on
// the exact minute value, not just presence.
func TestCalendarEventReminderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("calendar-reminder-test-secret")
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

	// Create a timed event with a 15-minute reminder.
	if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"Standup","start":"2026-08-02T09:00:00Z","reminderMinutes":15}`); rec.Code != http.StatusOK {
		t.Fatalf("create with reminder: status %d", rec.Code)
	}

	// Reload it the way the SPA does; the reminder must come back as 15.
	rec := do(http.MethodGet, "/api/v1/calendar/events", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d", rec.Code)
	}
	var listed struct {
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(listed.Events))
	}
	got := listed.Events[0].ReminderMinutes
	if got == nil {
		t.Fatal("listed event has no reminderMinutes, want 15 (VALARM round-trip lost it)")
	}
	if *got != 15 {
		t.Fatalf("reminderMinutes = %d, want 15", *got)
	}

	// Clear the reminder by updating without one; the reloaded event must have none.
	uid := listed.Events[0].UID
	if rec := do(http.MethodPut, "/api/v1/calendar/events/"+uid, `{"summary":"Standup","start":"2026-08-02T09:00:00Z"}`); rec.Code != http.StatusOK {
		t.Fatalf("update clearing reminder: status %d", rec.Code)
	}
	rec = do(http.MethodGet, "/api/v1/calendar/events", "")
	var after struct {
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode list after update: %v", err)
	}
	if len(after.Events) != 1 {
		t.Fatalf("got %d events after update, want 1", len(after.Events))
	}
	if after.Events[0].ReminderMinutes != nil {
		t.Fatalf("reminderMinutes = %d after update without reminder, want nil", *after.Events[0].ReminderMinutes)
	}
}

// TestCalendarSettingsRoundTrip proves the week-start setting persists in the
// shared webmail settings blob (DB-backed, per-user), not a client-side shortcut:
// a PUT then a fresh GET (the SPA's reload path) must return the stored weekday.
// A silent loss here would regress to a client-only store that does not survive a
// reload or apply cross-device, the shortcut the user rejected.
func TestCalendarSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("calendar-settings-test-secret")
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

	// A fresh account defaults to Monday (1) before any setting is stored.
	rec := do(http.MethodGet, "/api/v1/calendar/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get default: status %d", rec.Code)
	}
	var got calendarSettingsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode default: %v", err)
	}
	if got.FirstDayOfWeek != 1 {
		t.Fatalf("default firstDayOfWeek = %d, want 1 (Monday)", got.FirstDayOfWeek)
	}

	// Persist Sunday (0) and read it back the way the SPA does after a reload.
	if rec := do(http.MethodPut, "/api/v1/calendar/settings", `{"firstDayOfWeek":0}`); rec.Code != http.StatusOK {
		t.Fatalf("put: status %d", rec.Code)
	}
	rec = do(http.MethodGet, "/api/v1/calendar/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get after put: status %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode after put: %v", err)
	}
	if got.FirstDayOfWeek != 0 {
		t.Fatalf("firstDayOfWeek after put = %d, want 0 (Sunday, persisted in the settings blob)", got.FirstDayOfWeek)
	}

	// An out-of-range value is clamped to the Monday default, never stored as-is.
	if rec := do(http.MethodPut, "/api/v1/calendar/settings", `{"firstDayOfWeek":9}`); rec.Code != http.StatusOK {
		t.Fatalf("put invalid: status %d", rec.Code)
	}
	rec = do(http.MethodGet, "/api/v1/calendar/settings", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode after invalid: %v", err)
	}
	if got.FirstDayOfWeek != 1 {
		t.Fatalf("firstDayOfWeek after invalid = %d, want 1 (clamped to Monday)", got.FirstDayOfWeek)
	}
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

	secret := []byte("calendar-busy-test-secret")
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

	for _, want := range []int{0, 1, 2, 3} {
		// Create an event with this busy status, then reload and assert it survived.
		if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"Meeting","start":"2026-08-02T09:00:00Z","busyStatus":`+strconv.Itoa(want)+`}`); rec.Code != http.StatusOK {
			t.Fatalf("create busyStatus=%d: status %d", want, rec.Code)
		}
		rec := do(http.MethodGet, "/api/v1/calendar/events", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list: status %d", rec.Code)
		}
		var listed struct {
			Events []eventJSON `json:"events"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(listed.Events) != 1 {
			t.Fatalf("got %d events, want 1", len(listed.Events))
		}
		got := listed.Events[0].BusyStatus
		if got == nil {
			t.Fatalf("busyStatus=%d: listed event has no busyStatus (round-trip lost it)", want)
		}
		if *got != want {
			t.Fatalf("busyStatus = %d, want %d (oof would be lost via the iCal path)", *got, want)
		}
		// Clear it for the next iteration so only one event is present.
		uid := listed.Events[0].UID
		if rec := do(http.MethodDelete, "/api/v1/calendar/events/"+uid, ""); rec.Code != http.StatusOK {
			t.Fatalf("delete: status %d", rec.Code)
		}
	}
}

// TestCalendarEventSensitivityRoundTrip proves the sensitivity (normal/private/
// confidential) round-trips through the iCal CLASS property. Private and
// confidential must survive a reload; normal stays unset (absent) so the form
// shows the default.
func TestCalendarEventSensitivityRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("calendar-sensitivity-test-secret")
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

	for _, want := range []int{2, 3} {
		if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"Meeting","start":"2026-08-02T09:00:00Z","sensitivity":`+strconv.Itoa(want)+`}`); rec.Code != http.StatusOK {
			t.Fatalf("create sensitivity=%d: status %d", want, rec.Code)
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
		got := listed.Events[0].Sensitivity
		if got == nil {
			t.Fatalf("sensitivity=%d: listed event has no sensitivity (CLASS round-trip lost it)", want)
		}
		if *got != want {
			t.Fatalf("sensitivity = %d, want %d", *got, want)
		}
		uid := listed.Events[0].UID
		if rec := do(http.MethodDelete, "/api/v1/calendar/events/"+uid, ""); rec.Code != http.StatusOK {
			t.Fatalf("delete: status %d", rec.Code)
		}
	}

	// A normal (sensitivity unset) event must not surface a sensitivity after reload.
	if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"Open","start":"2026-08-02T09:00:00Z"}`); rec.Code != http.StatusOK {
		t.Fatalf("create normal: status %d", rec.Code)
	}
	rec := do(http.MethodGet, "/api/v1/calendar/events", "")
	var listed struct {
		Events []eventJSON `json:"events"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	if len(listed.Events) != 1 || listed.Events[0].Sensitivity != nil {
		t.Fatalf("normal event has sensitivity = %v, want nil", listed.Events[0].Sensitivity)
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
