package webmail2api

import (
	"net/http"
	"testing"
)

// TestNoteColorRoundTrip proves a sticky note's color (PidLidNoteColor) survives
// a create-then-reload cycle, and that a note with no color defaults to yellow
// (3, the Outlook default).
func TestNoteColorRoundTrip(t *testing.T) {
	do := apiDo(t)

	// Create a green (1) note and a default (no color) note.
	wantStatus(t, "create green", do(http.MethodPost, "/api/v1/notes",
		`{"title":"Groceries","body":"Milk","color":1}`), http.StatusOK)
	wantStatus(t, "create default", do(http.MethodPost, "/api/v1/notes",
		`{"title":"Idea","body":"Ship it"}`), http.StatusOK)

	type listing struct {
		Notes []noteJSON `json:"notes"`
	}
	listed := okBody[listing](t, "list", do(http.MethodGet, "/api/v1/notes", ""))
	if len(listed.Notes) != 2 {
		t.Fatalf("got %d notes, want 2", len(listed.Notes))
	}
	byTitle := map[string]int{}
	for _, n := range listed.Notes {
		byTitle[n.Title] = n.Color
	}
	wantEq(t, "Groceries color (green)", byTitle["Groceries"], 1)
	wantEq(t, "Idea color (yellow default)", byTitle["Idea"], 3)
}

// TestNoteUpdateKeepsItsID proves editing a note keeps the note: the same id, the
// new text, and the color the edit did not name. The update deleted the note and
// created another, so every EWS, ActiveSync and Outlook client holding the old id
// saw the note vanish and a new one appear.
func TestNoteUpdateKeepsItsID(t *testing.T) {
	do := apiDo(t)
	created := okBody[noteJSON](t, "create", do(http.MethodPost, "/api/v1/notes",
		`{"title":"Groceries","body":"Milk","color":1}`))
	updated := okBody[noteJSON](t, "update", do(http.MethodPut, "/api/v1/notes/"+created.ID,
		`{"title":"Groceries","body":"Milk and eggs"}`))
	wantEq(t, "the updated id", updated.ID, created.ID)

	type listing struct {
		Notes []noteJSON `json:"notes"`
	}
	listed := okBody[listing](t, "list", do(http.MethodGet, "/api/v1/notes", ""))
	if len(listed.Notes) != 1 {
		t.Fatalf("got %d notes, want the one edited note", len(listed.Notes))
	}
	wantEq(t, "the id", listed.Notes[0].ID, created.ID)
	wantEq(t, "the body", listed.Notes[0].Body, "Milk and eggs")
	wantEq(t, "the kept color", listed.Notes[0].Color, 1)
	wantStatus(t, "an unknown note", do(http.MethodPut, "/api/v1/notes/999999", `{"title":"x"}`), http.StatusNotFound)
	for _, tc := range []struct{ path, body string }{
		{"/api/v1/notes/not-a-number", `{"title":"x"}`},
		{"/api/v1/notes/" + created.ID, `{`},
	} {
		if rec := do(http.MethodPut, tc.path, tc.body); rec.Code == http.StatusOK {
			t.Errorf("PUT %s %s answered 200", tc.path, tc.body)
		}
	}
	after := okBody[listing](t, "list after refused edits", do(http.MethodGet, "/api/v1/notes", ""))
	wantEq(t, "body after refused edits", after.Notes[0].Body, "Milk and eggs")
}

// TestNoteColorOutsideTheEnumIsDropped: PidLidNoteColor is 0 (blue) to 4
// (yellow) in MS-OXONOTE, so another value is not a colour any client can show
// and the note keeps the default.
func TestNoteColorOutsideTheEnumIsDropped(t *testing.T) {
	do := apiDo(t)
	for _, c := range []string{"5", "-1", "99"} {
		wantStatus(t, "create colour "+c, do(http.MethodPost, "/api/v1/notes", `{"title":"c`+c+`","color":`+c+`}`), http.StatusOK)
	}
	listed := okBody[struct {
		Notes []noteJSON `json:"notes"`
	}](t, "list", do(http.MethodGet, "/api/v1/notes", ""))
	for _, n := range listed.Notes {
		if n.Color < 0 || n.Color > 4 {
			t.Errorf("%s stored colour %d, outside MS-OXONOTE's 0..4", n.Title, n.Color)
		}
	}
}
