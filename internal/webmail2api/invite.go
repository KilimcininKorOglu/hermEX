package webmail2api

import (
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mime"
	"hermex/internal/mta"
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

// handleExportICS streams a message's embedded meeting invite as an .ics file.
// The calendar part carries the original iTIP METHOD, VEVENT, and any VTIMEZONE,
// so it is served verbatim rather than round-tripped through oxcical (which would
// drop the METHOD and other iTIP-only fields). Messages without a calendar part
// are not invites and return 404.
func (s *Server) handleExportICS(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	ics := findCalendarPart(mime.ParseStructure(raw))
	if ics == nil {
		http.Error(w, "no calendar invite in message", http.StatusNotFound)
		return
	}
	name := icsFilename(ics)
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	_, _ = w.Write(ics)
}

// icsFilename derives a safe download name from the invite's SUMMARY (falling
// back to its UID, then a constant), always ending in ".ics".
func icsFilename(ics []byte) string {
	base, _ := icalProp(ics, "SUMMARY")
	if strings.TrimSpace(base) == "" {
		base, _ = icalProp(ics, "UID")
	}
	base = sanitizeICSName(base)
	if base == "" {
		base = "invite"
	}
	return base + ".ics"
}

// sanitizeICSName keeps a filename safe for a Content-Disposition header: it
// drops path separators, quotes, and control characters, collapses whitespace to
// underscores, and bounds the length.
func sanitizeICSName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			// skip control characters
		case r == '/' || r == '\\' || r == '"' || r == ':':
			// skip path and quoting characters
		case r == ' ' || r == '\t':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
		if b.Len() >= 80 {
			break
		}
	}
	return strings.Trim(b.String(), "_.")
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
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
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
	// Email the iTIP REPLY back to the organizer so they can track the response
	// (the TrackingTab the organizer's event shows). Best-effort: a delivery
	// failure does not undo the calendar add.
	organizer, _ := icalProp(ics, "ORGANIZER")
	if org := organizerAddress(organizer); org != "" {
		if msg, berr := buildReplyRequest(c.Email, org, icalToEvent(ics, 0), req.Response); berr == nil {
			if _, derr := mta.DeliverAndRelay(s.accounts, s.spool, c.Email, []string{org}, msg, time.Now()); derr != nil {
				logError("send-meeting-reply", derr, logging.Fields{"user": c.Email, "organizer": org})
			}
			fileSentCopy(st, msg, c.Email, "meeting-reply")
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": req.Response + "ed"})
}

// partstatFor maps the SPA's response vocabulary to the iCalendar PARTSTAT an
// iTIP REPLY carries (RFC 5546 §3.2.12).
func partstatFor(response string) string {
	switch response {
	case "accept":
		return "ACCEPTED"
	case "tentative":
		return "TENTATIVE"
	case "decline":
		return "DECLINED"
	}
	return "NEEDS-ACTION"
}

// buildReplyRequest renders a METHOD:REPLY iTIP message: the attendee tells the
// organizer their response (PARTSTAT). The VEVENT carries the original UID and a
// single ATTENDEE (the responder) with its PARTSTAT, addressed to the organizer.
func buildReplyRequest(responder, organizer string, e eventJSON, response string) ([]byte, error) {
	addr := strings.TrimSpace(organizer)
	if parsed, err := mail.ParseAddress(addr); err == nil {
		addr = parsed.Address
	}
	if addr == "" {
		return nil, fmt.Errorf("no organizer address")
	}
	var cal strings.Builder
	cal.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//webmail2//EN\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\n")
	fmt.Fprintf(&cal, "UID:%s\r\n", uidOrGenerated(e.UID))
	fmt.Fprintf(&cal, "SUMMARY:%s\r\n", e.Summary)
	fmt.Fprintf(&cal, "DTSTART%s\r\n", toICalTime(e.Start, e.AllDay))
	if e.End != "" {
		fmt.Fprintf(&cal, "DTEND%s\r\n", toICalTime(e.End, e.AllDay))
	}
	fmt.Fprintf(&cal, "ORGANIZER;CN=%s:mailto:%s\r\n", organizer, organizer)
	fmt.Fprintf(&cal, "ATTENDEE;CN=%s;PARTSTAT=%s:mailto:%s\r\n", responder, partstatFor(response), responder)
	cal.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	textBody := fmt.Sprintf("%s has %s the meeting: %s", responder, response, e.Summary)
	boundary := "hermex-reply-" + randomHex()
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", responder)
	fmt.Fprintf(&b, "To: %s\r\n", addr)
	fmt.Fprintf(&b, "Subject: %s: %s\r\n", response, e.Summary)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@hermex>\r\n", randomHex())
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(textBody)
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/calendar; method=REPLY; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(cal.String())
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return []byte(b.String()), nil
}

// buildCounterRequest renders a METHOD:COUNTER iTIP message (RFC 5546 §3.6): an
// invitee proposes a new time to the organizer. The iCalendar carries the
// original meeting UID and the proposed start/end; the MIME body is a plain-text
// note plus the text/calendar;method=COUNTER part. It is addressed to the
// organizer only and returns the raw message.
func buildCounterRequest(proposer, organizer string, e eventJSON) ([]byte, error) {
	addr := strings.TrimSpace(organizer)
	if parsed, err := mail.ParseAddress(addr); err == nil {
		addr = parsed.Address
	}
	if addr == "" {
		return nil, fmt.Errorf("no organizer address")
	}
	var cal strings.Builder
	cal.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//webmail2//EN\r\nMETHOD:COUNTER\r\nBEGIN:VEVENT\r\n")
	fmt.Fprintf(&cal, "UID:%s\r\n", uidOrGenerated(e.UID))
	fmt.Fprintf(&cal, "SUMMARY:%s\r\n", e.Summary)
	fmt.Fprintf(&cal, "DTSTART%s\r\n", toICalTime(e.Start, e.AllDay))
	if e.End != "" {
		fmt.Fprintf(&cal, "DTEND%s\r\n", toICalTime(e.End, e.AllDay))
	}
	fmt.Fprintf(&cal, "ORGANIZER;CN=%s:mailto:%s\r\n", organizer, organizer)
	fmt.Fprintf(&cal, "ATTENDEE;CN=%s;ROLE=REQ-PARTICIPANT:mailto:%s\r\n", proposer, proposer)
	cal.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")

	textBody := fmt.Sprintf("%s proposed a new time for: %s\r\nProposed: %s", proposer, e.Summary, e.Start)
	boundary := "hermex-counter-" + randomHex()
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", proposer)
	fmt.Fprintf(&b, "To: %s\r\n", addr)
	fmt.Fprintf(&b, "Subject: Proposed new time: %s\r\n", e.Summary)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@hermex>\r\n", randomHex())
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(textBody)
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/calendar; method=COUNTER; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(cal.String())
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return []byte(b.String()), nil
}

// handleProposeTime lets an invitee propose a new time for a meeting: it reads
// the invite message for the original meeting identity + organizer, then emails a
// METHOD:COUNTER iTIP to the organizer with the proposed start/end. The proposal
// does not mutate the invitee's calendar (the organizer must accept the counter).
func (s *Server) handleProposeTime(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req struct {
		ID    string `json:"id"`
		Start string `json:"start"`
		End   string `json:"end"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == "" || req.Start == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, fid, uid, ok := s.locate(w, r, req.ID)
	if !ok {
		return
	}
	defer st.Close()
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
	e := icalToEvent(ics, 0)
	e.Start = req.Start
	e.End = req.End
	organizer, _ := icalProp(ics, "ORGANIZER")
	org := organizerAddress(organizer)
	msg, berr := buildCounterRequest(c.Email, org, e)
	if berr != nil {
		logError("build-counter-proposal", berr, logging.Fields{"user": c.Email, "organizer": org})
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not build the counter-proposal"})
		return
	}
	if _, err := mta.DeliverAndRelay(s.accounts, s.spool, c.Email, []string{org}, msg, time.Now()); err != nil {
		logError("send-counter-proposal", err, logging.Fields{"user": c.Email, "organizer": org})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delivery failed"})
		return
	}
	// File a Sent copy so the invitee sees the outgoing counter-proposal.
	fileSentCopy(st, msg, c.Email, "counter-proposal")
	writeJSON(w, http.StatusOK, map[string]string{"status": "proposed"})
}
