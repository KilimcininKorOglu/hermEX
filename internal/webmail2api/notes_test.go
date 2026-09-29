package webmail2api

import (
	"net/http"
	"testing"
)

// TestNoteColorRoundTrip proves a sticky note's color (PidLidNoteColor) survives
// a create-then-reload cycle, and that a note with no color defaults to yellow
// (3, the Outlook default).
func TestNoteColorRoundTrip(t *testing.T) {
	do, _ := apiHarness(t)

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
	do, _ := apiHarness(t)
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
}
