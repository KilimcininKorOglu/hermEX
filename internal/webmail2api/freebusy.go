package webmail2api

import (
	"net/http"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/ews"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// roomLister is the optional directory capability that lists bookable resource
// mailboxes for the room picker. SQLDirectory implements it; absent (static
// accounts) yields an empty room list.
type roomLister interface {
	ListRooms() ([]directory.GALEntry, error)
}

type roomJSON struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Capacity int    `json:"capacity,omitempty"`
}

// handleRooms lists the organization's bookable rooms for the room picker.
func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	lister, ok := s.auth.(roomLister)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"rooms": []roomJSON{}})
		return
	}
	entries, err := lister.ListRooms()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	rooms := make([]roomJSON, 0, len(entries))
	for _, e := range entries {
		rooms = append(rooms, roomJSON{Email: e.Address, Name: e.DisplayName, Capacity: e.Capacity})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": rooms})
}

// freeBusyPerms mirrors the EWS GetUserAvailability gate: a non-owner sees a target's
// busy blocks only with a free/busy or read-any right on that calendar. Without it
// the data is not leaked (returned as no busy blocks), never shown as all-free.
const freeBusyPerms = mapi.FrightsFreeBusySimple | mapi.FrightsFreeBusyDetailed | mapi.FrightsReadAny

type freeBusyJSON struct {
	User string     `json:"user"`
	Busy []busyJSON `json:"busy"`
}

type busyJSON struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// handleFreeBusy returns each requested user's busy intervals within a window, read
// from their real calendar and gated by the same free/busy permission EWS enforces,
// so the attendee picker can show a free/busy dot without exposing event details.
func (s *Server) handleFreeBusy(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	q := r.URL.Query()
	start, err1 := time.Parse(time.RFC3339, q.Get("start"))
	end, err2 := time.Parse(time.RFC3339, q.Get("end"))
	if err1 != nil || err2 != nil || !end.After(start) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad window"})
		return
	}
	out := make([]freeBusyJSON, 0)
	for _, email := range splitCSV(q.Get("users")) {
		out = append(out, freeBusyJSON{User: email, Busy: s.busyFor(c, email, start, end)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"freeBusy": out})
}

// busyFor returns a target's busy intervals when the caller owns the mailbox or holds
// a free/busy right on its calendar; otherwise an empty set, so a caller without
// permission learns nothing about the target's calendar (OWASP A01).
func (s *Server) busyFor(c sessionClaims, email string, start, end time.Time) []busyJSON {
	targetPath, ok := s.accounts.Resolve(email)
	if !ok {
		return []busyJSON{}
	}
	st, err := objectstore.Open(targetPath)
	if err != nil {
		return []busyJSON{}
	}
	defer st.Close()

	owner := strings.EqualFold(email, c.Email) || targetPath == c.Mailbox
	if !owner {
		perm, err := st.ResolvePermission(int64(mapi.PrivateFIDCalendar), c.Email)
		if err != nil || perm&freeBusyPerms == 0 {
			return []busyJSON{}
		}
	}

	// webmail2 shows only busy blocks (no event detail), so the detailed view is never
	// requested even for the owner.
	events, err := ews.CalendarFreeBusy(st, start, end, false)
	if err != nil {
		return []busyJSON{}
	}
	busy := make([]busyJSON, 0, len(events))
	for _, ev := range events {
		busy = append(busy, busyJSON{Start: ev.StartTime, End: ev.EndTime})
	}
	return busy
}

// splitCSV splits a comma-separated list into trimmed, non-empty values.
func splitCSV(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
