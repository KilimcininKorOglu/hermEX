package webmail2api

import (
	"net/http"
	"strings"
	"time"

	"fmt"
	"hermex/internal/directory"
	"hermex/internal/ews"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"sync/atomic"
)

// roomLister is the optional directory capability that lists bookable resource
// mailboxes for the room picker. SQLDirectory implements it; absent (static
// accounts) yields an empty room list. ListRooms is caller-scoped and fails closed,
// so the signature must carry the authenticated caller, matching the concrete
// SQLDirectory/StaticAccounts methods; a no-arg declaration never satisfies either
// and the type assertion silently misses, leaving the picker empty.
type roomLister interface {
	ListRooms(caller string) ([]directory.GALEntry, error)
}

type roomJSON struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Capacity int    `json:"capacity,omitempty"`
}

// handleRooms lists the organization's bookable rooms for the room picker.
func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	lister, ok := s.auth.(roomLister)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"rooms": []roomJSON{}})
		return
	}
	// ListRooms is caller-scoped and fails closed: an empty caller returns nothing.
	entries, err := lister.ListRooms(c.Email)
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
	// Each target opens a store and scans a whole calendar, and the query string
	// admits thousands of entries, so answer only the first N rather than letting
	// one request drive unbounded disk work.
	users := splitCSV(q.Get("users"))
	if max := maxFreeBusyTargets(); len(users) > max {
		logError("freebusy-truncated", fmt.Errorf("asked for %d targets, cap is %d", len(users), max),
			logging.Fields{"user": c.Email})
		users = users[:max]
	}
	out := make([]freeBusyJSON, 0)
	for _, email := range users {
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
		// The grid shows time that is TAKEN. An appointment marked free, or one
		// marking a home office day (working elsewhere), says where the attendee is
		// rather than that they are unavailable, so reporting it as busy would empty
		// the day of candidate times.
		if !mapi.BusyStatusOccupies(ev.Status) {
			continue
		}
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

// defaultFreeBusyTargets caps how many mailboxes one availability request may fan
// out to when no operator limit is set. Each target costs a store open and a full
// calendar scan, and the request admits far more list entries than that, so the
// count needs its own bound; the byte cap does not provide one.
const defaultFreeBusyTargets = 100

// freeBusyTargetLimit holds the operator-set availability target cap (0 = use the
// default), set by SetMaxFreeBusyTargets and read live per request.
var freeBusyTargetLimit atomic.Int64

// SetMaxFreeBusyTargets sets how many mailboxes one availability request may fan out
// to (0 restores the built-in default). It is safe to call concurrently with request
// handling, so an operator's edit applies without a restart.
func SetMaxFreeBusyTargets(n int64) {
	if n < 0 {
		n = 0
	}
	freeBusyTargetLimit.Store(n)
}

// maxFreeBusyTargets returns the cap in force.
func maxFreeBusyTargets() int {
	if n := freeBusyTargetLimit.Load(); n > 0 {
		return int(n)
	}
	return defaultFreeBusyTargets
}
