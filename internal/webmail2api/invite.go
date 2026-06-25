package webmail2api

import (
	"net/http"
	"strings"

	"hermex/internal/mime"
)

// findCalendarPart returns the decoded iCalendar (text/calendar or an .ics
// attachment) carried by a message, or nil when it holds no invite.
func findCalendarPart(root *mime.Part) []byte {
	var found []byte
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if p == nil || found != nil {
			return
		}
		isCal := (p.Type == "text" && p.Subtype == "calendar") ||
			(p.Type == "application" && p.Subtype == "ics")
		if isCal {
			if c, err := p.DecodedContent(); err == nil {
				found = c
				return
			}
		}
		for _, ch := range p.Children {
			walk(ch)
		}
	}
	walk(root)
	return found
}

// organizerAddress extracts the SMTP address from an ORGANIZER value, which is
// usually "mailto:user@host" but may be a bare address.
func organizerAddress(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.LastIndex(strings.ToLower(v), "mailto:"); i >= 0 {
		return strings.TrimSpace(v[i+len("mailto:"):])
	}
	return v
}

// handleInvite reports whether a message is a meeting invite and, when it is,
// the details parsed from its embedded iCalendar.
func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"isInvite": false})
		return
	}
	ics := findCalendarPart(mime.ParseStructure(raw))
	if ics == nil {
		writeJSON(w, http.StatusOK, map[string]any{"isInvite": false})
		return
	}
	e := icalToEvent(ics, 0)
	organizer, _ := icalProp(ics, "ORGANIZER")
	writeJSON(w, http.StatusOK, map[string]any{
		"isInvite":  true,
		"uid":       e.UID,
		"summary":   e.Summary,
		"start":     e.Start,
		"end":       e.End,
		"location":  e.Location,
		"organizer": organizerAddress(organizer),
	})
}

// handleRSVP responds to a meeting invite: accept and tentative add the event to
// the calendar; decline only acknowledges. (Mailing the iTIP reply back to the
// organizer is a follow-up.)
func (s *Server) handleRSVP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string `json:"id"`
		Response string `json:"response"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, fid, uid, ok := s.locate(w, r, req.ID)
	if !ok {
		return
	}
	defer st.Close()
	if req.Response == "decline" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "declined"})
		return
	}
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	ics := findCalendarPart(mime.ParseStructure(raw))
	if ics == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a meeting invite"})
		return
	}
	// An accepted invite lands in the default calendar.
	if _, err := storeEvent(st, icalToEvent(ics, 0), calendarFolderID("")); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not add to calendar"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": req.Response + "ed"})
}
