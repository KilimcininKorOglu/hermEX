package webmail2api

import (
	"net/http"
	"strconv"
	"testing"
)

// occurrencePath is the instance endpoint of the series with message id id.
func occurrencePath(id int64) string {
	return "/api/v1/calendar/events/" + strconv.FormatInt(id, 10) + "/occurrence"
}

// windowStarts lists the instance starts from 29 March to 1 April.
func windowStarts(t *testing.T, do requestFunc) []string {
	t.Helper()
	var out []string
	for _, r := range listWindow(t, do, "2026-03-29T00:00:00Z", "2026-04-01T00:00:00Z") {
		out = append(out, r.Start)
	}
	return out
}

// wantStartList holds a listing's instance starts.
func wantStartList(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: starts = %v, want %v", what, got, want)
	}
	for i := range want {
		wantEq(t, what, got[i], want[i])
	}
}

// TestMoveOccurrenceMovesOneInstance drags the 30 March instance to 11:00 UTC and
// then again to 14:00: the other days stay at 09:00 Berlin, and the second move
// replaces the first. The listing still names the moved row by its generated
// instant.
func TestMoveOccurrenceMovesOneInstance(t *testing.T) {
	do, _ := apiHarness(t)
	id := createEvent(t, do, berlinStandup)

	wantStatus(t, "move", do(http.MethodPut, occurrencePath(id),
		`{"occurrence":"2026-03-30T07:00:00Z","start":"2026-03-30T11:00:00Z","end":"2026-03-30T11:30:00Z"}`), http.StatusOK)
	wantStartList(t, "after move", windowStarts(t, do), "2026-03-29T07:00:00Z", "2026-03-30T11:00:00Z", "2026-03-31T07:00:00Z")

	wantStatus(t, "move again", do(http.MethodPut, occurrencePath(id),
		`{"occurrence":"2026-03-30T07:00:00Z","start":"2026-03-30T14:00:00Z","end":"2026-03-30T14:30:00Z"}`), http.StatusOK)
	rows := listWindow(t, do, "2026-03-30T00:00:00Z", "2026-03-31T00:00:00Z")
	if len(rows) != 1 {
		t.Fatalf("30 March rows = %+v, want 1", rows)
	}
	wantEq(t, "moved start", rows[0].Start, "2026-03-30T14:00:00Z")
	wantEq(t, "moved occurrence", rows[0].Occurrence, "2026-03-30T07:00:00Z")
	wantEq(t, "moved title", rows[0].Summary, "Standup")
}

// TestDeleteOccurrenceRemovesOneInstance deletes the 30 March instance: the days
// around it stay, and deleting it again finds nothing.
func TestDeleteOccurrenceRemovesOneInstance(t *testing.T) {
	do, _ := apiHarness(t)
	id := createEvent(t, do, berlinStandup)

	wantStatus(t, "delete", do(http.MethodDelete, occurrencePath(id)+"?at=2026-03-30T07:00:00Z", ""), http.StatusOK)
	wantStartList(t, "after delete", windowStarts(t, do), "2026-03-29T07:00:00Z", "2026-03-31T07:00:00Z")
	wantStatus(t, "delete again", do(http.MethodDelete, occurrencePath(id)+"?at=2026-03-30T07:00:00Z", ""), http.StatusNotFound)
}

// TestOccurrenceEditRefusesWhatIsNotAnInstance answers 404 for an instant off the
// pattern, for a single event and for an unknown id, and 400 for a bad instant.
func TestOccurrenceEditRefusesWhatIsNotAnInstance(t *testing.T) {
	do, _ := apiHarness(t)
	series := createEvent(t, do, berlinStandup)
	single := createEvent(t, do, `{"summary":"Once","start":"2026-03-30T07:00:00Z","end":"2026-03-30T08:00:00Z"}`)
	move := `{"occurrence":"2026-03-30T07:00:00Z","start":"2026-03-30T11:00:00Z","end":"2026-03-30T11:30:00Z"}`

	wantStatus(t, "off pattern", do(http.MethodPut, occurrencePath(series),
		`{"occurrence":"2026-03-30T08:00:00Z","start":"2026-03-30T11:00:00Z","end":"2026-03-30T11:30:00Z"}`), http.StatusNotFound)
	wantStatus(t, "single event", do(http.MethodPut, occurrencePath(single), move), http.StatusNotFound)
	wantStatus(t, "unknown id", do(http.MethodPut, occurrencePath(1<<40), move), http.StatusNotFound)
	wantStatus(t, "bad instant", do(http.MethodDelete, occurrencePath(series)+"?at=soon", ""), http.StatusBadRequest)
}
