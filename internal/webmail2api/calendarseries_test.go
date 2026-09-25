package webmail2api

import (
	"net/http"
	"strings"
	"testing"
)

// berlinStandup is a daily 09:00 Berlin series that starts two days before the
// 29 March 2026 switch to summer time.
const berlinStandup = `{"summary":"Standup","start":"2026-03-27T08:00:00Z","end":"2026-03-27T08:30:00Z",` +
	`"recurrence":"FREQ=DAILY","timezone":"Europe/Berlin"}`

// listWindow reads the listing for [start, end) the way the SPA asks for a view.
func listWindow(t *testing.T, do requestFunc, start, end string) []eventJSON {
	t.Helper()
	type listing struct {
		Events []eventJSON `json:"events"`
	}
	return okBody[listing](t, "list "+start, do(http.MethodGet, "/api/v1/calendar/events?start="+start+"&end="+end, "")).Events
}

// TestEventKeepsItsRecurrenceAndZone reads back what the editor saved: the rule,
// the zone and the series' first start.
func TestEventKeepsItsRecurrenceAndZone(t *testing.T) {
	do, _ := apiHarness(t)
	wantStatus(t, "create", do(http.MethodPost, "/api/v1/calendar/events", berlinStandup), http.StatusOK)

	e := listOneEvent(t, do, "list")
	wantEq(t, "recurrence", e.Recurrence, "FREQ=DAILY")
	wantEq(t, "timezone", e.Timezone, "Europe/Berlin")
	wantEq(t, "start", e.Start, "2026-03-27T08:00:00Z")
	wantEq(t, "occurrence on an unexpanded row", e.Occurrence, "")
}

// TestWindowExpandsASeriesOnItsWallClock lists three days across the switch: one
// row per instance, all at 09:00 Berlin, so 08:00 UTC before the switch and 07:00
// UTC after it, each naming its instance and the series it belongs to.
func TestWindowExpandsASeriesOnItsWallClock(t *testing.T) {
	do, _ := apiHarness(t)
	wantStatus(t, "create", do(http.MethodPost, "/api/v1/calendar/events", berlinStandup), http.StatusOK)

	rows := listWindow(t, do, "2026-03-28T00:00:00Z", "2026-03-31T00:00:00Z")
	want := []string{"2026-03-28T08:00:00Z", "2026-03-29T07:00:00Z", "2026-03-30T07:00:00Z"}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, r := range rows {
		wantEq(t, "instance start", r.Start, want[i])
		wantEq(t, "instance occurrence", r.Occurrence, want[i])
		wantEq(t, "series start", r.SeriesStart, "2026-03-27T08:00:00Z")
		wantEq(t, "row uid", r.UID, rows[0].UID)
	}
}

// TestWindowFindsASeriesThatStartedBefore lists a June day: the series began in
// March, so its master's own times lie outside the window and only the recurring
// flag brings it in.
func TestWindowFindsASeriesThatStartedBefore(t *testing.T) {
	do, _ := apiHarness(t)
	wantStatus(t, "create", do(http.MethodPost, "/api/v1/calendar/events", berlinStandup), http.StatusOK)

	rows := listWindow(t, do, "2026-06-01T00:00:00Z", "2026-06-02T00:00:00Z")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want the 1 June instance: %+v", len(rows), rows)
	}
	wantEq(t, "June instance", rows[0].Start, "2026-06-01T07:00:00Z")
}

// TestSingleEventKeepsItsZone reads the zone of an event that does not recur,
// whose export is in UTC and whose zone therefore comes from the stored display
// time zone.
func TestSingleEventKeepsItsZone(t *testing.T) {
	do, _ := apiHarness(t)
	wantStatus(t, "create", do(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Lunch","start":"2026-08-02T09:00:00Z","end":"2026-08-02T10:00:00Z","timezone":"Europe/Istanbul"}`), http.StatusOK)

	e := listOneEvent(t, do, "list")
	wantEq(t, "timezone", e.Timezone, "Europe/Istanbul")
	wantEq(t, "start", e.Start, "2026-08-02T09:00:00Z")
}

// TestSeriesExportCarriesItsZone downloads the series as .ics: the file names the
// zone and describes it, so the importing client places 09:00 in Berlin.
func TestSeriesExportCarriesItsZone(t *testing.T) {
	do, _ := apiHarness(t)
	wantStatus(t, "create", do(http.MethodPost, "/api/v1/calendar/events", berlinStandup), http.StatusOK)
	uid := listOneEvent(t, do, "list").UID

	rec := do(http.MethodGet, "/api/v1/calendar/events/"+uid+"/ics", "")
	wantStatus(t, "export", rec, http.StatusOK)
	body := rec.Body.String()
	for _, want := range []string{"BEGIN:VTIMEZONE", "TZID:Europe/Berlin", "DTSTART;TZID=Europe/Berlin:20260327T090000", "RRULE:FREQ=DAILY"} {
		if !strings.Contains(body, want) {
			t.Errorf("export lacks %q:\n%s", want, body)
		}
	}
}

// TestEventRefusesARuleThatIsNotOne keeps a line break in the recurrence from
// adding lines of the sender's choosing to the stored calendar.
func TestEventRefusesARuleThatIsNotOne(t *testing.T) {
	do, _ := apiHarness(t)
	for _, rule := range []string{`FREQ=DAILY\r\nATTENDEE:mailto:x@example.test`, "FREQ=DAILY;TZID=X:1", "SOMETIMES"} {
		wantStatus(t, "create "+rule, do(http.MethodPost, "/api/v1/calendar/events",
			`{"summary":"x","start":"2026-08-02T09:00:00Z","recurrence":"`+rule+`"}`), http.StatusBadRequest)
	}
	wantEq(t, "events stored", len(listEvents(t, do, "list")), 0)
}
