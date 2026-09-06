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
