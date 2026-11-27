package webmail2api

import (
	"math"
	"net/http"
	"testing"
)

// TestFitsMAPILongPinsTheBoundary pins the width guard applied to JSON integers
// that land in 32-bit MAPI longs.
func TestFitsMAPILongPinsTheBoundary(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want bool
	}{
		{"zero", 0, true},
		{"ordinary", 3, true},
		{"int32 max", math.MaxInt32, true},
		{"int32 min", math.MinInt32, true},
		{"one past int32 max", math.MaxInt32 + 1, false},
		{"one past int32 min", math.MinInt32 - 1, false},
	}
	for _, tc := range cases {
		if got := fitsMAPILong(tc.in); got != tc.want {
			t.Errorf("%s: fitsMAPILong(%d) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestNoteColorRejectsWiderThanPtLong is the guard at a call site. JSON carries no
// integer width, so a color of 4294967297 used to wrap to 1 and the note came back
// green: a setting the client never chose, indistinguishable from one it did. The
// value is now dropped, so the note reads back as the yellow default.
func TestNoteColorRejectsWiderThanPtLong(t *testing.T) {
	do := apiDo(t)
	// 4294967297 wraps to 1 (green) and -4294967295 to 1 as well in 32 bits.
	for _, c := range []string{"4294967297", "-4294967295"} {
		wantStatus(t, "create with colour "+c, do(http.MethodPost, "/api/v1/notes", `{"title":"Wide","body":"x","color":`+c+`}`), http.StatusOK)
	}
	listed := okBody[struct {
		Notes []noteJSON `json:"notes"`
	}](t, "list notes", do(http.MethodGet, "/api/v1/notes", ""))
	if len(listed.Notes) != 2 {
		t.Fatalf("got %d notes, want 2", len(listed.Notes))
	}
	for _, n := range listed.Notes {
		if n.Color != 3 {
			t.Errorf("color = %d, want 3 (the yellow default; %d means the value wrapped)", n.Color, n.Color)
		}
	}
}
