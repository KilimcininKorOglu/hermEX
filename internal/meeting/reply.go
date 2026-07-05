package meeting

import (
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// ProcessReply processes an inbound iTIP REPLY on the organizer's side: it reads
// the delivered message, extracts the text/calendar part, and if it is a
// METHOD:REPLY it parses the responder's PARTSTAT and updates that attendee's
// PidLidResponseStatus on the organizer's calendar event (matched by the REPLY's
// iCalendar UID). It reports whether a REPLY was handled.
//
// It is best-effort and never errors a delivery: a malformed or unmatched REPLY
// is left as an ordinary email the organizer can read, not a delivery failure.
func ProcessReply(st *objectstore.Store, messageID int64) bool {
	raw, err := st.GetMessageRaw(mapi.PrivateFIDInbox, uint32(messageID))
	if err != nil {
		return false
	}
	ics := findCalendarPart(mime.ParseStructure(raw))
	if ics == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(icalLine(ics, "METHOD")), "REPLY") {
		return false
	}
	uid := strings.TrimSpace(icalLine(ics, "UID"))
	attendee, partstat := parseAttendee(ics)
	if uid == "" || attendee == "" {
		return false
	}
	tags, err := ResolveTags(st)
	if err != nil {
		return false
	}
	resp := partstatResponse(partstat)
	if resp == 0 {
		return false
	}
	_ = ApplyReply(st, tags, uid, attendee, resp)
	return true
}

// findCalendarPart returns the decoded text/calendar (or .ics) body, or nil.
func findCalendarPart(root *mime.Part) []byte {
	var found []byte
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if p == nil || found != nil {
			return
		}
		if (p.Type == "text" && p.Subtype == "calendar") || (p.Type == "application" && p.Subtype == "ics") {
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

// icalLine returns the value of the first top-level property named name in the
// iCalendar stream (ignoring parameters and folding), or "". It is a minimal
// scanner sufficient for REPLY's METHOD/UID, not a general parser.
func icalLine(ics []byte, name string) string {
	for line := range strings.SplitSeq(string(ics), "\n") {
		line = strings.TrimRight(line, "\r")
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if semi := strings.IndexByte(key, ';'); semi >= 0 {
			key = key[:semi]
		}
		if strings.EqualFold(key, name) {
			return val
		}
	}
	return ""
}

// parseAttendee reads the ATTENDEE line's CN (the responder's address, falling
// back to the mailto: value) and PARTSTAT. The REPLY carries a single attendee.
func parseAttendee(ics []byte) (addr, partstat string) {
	for line := range strings.SplitSeq(string(ics), "\n") {
		line = strings.TrimRight(line, "\r")
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if semi := strings.IndexByte(key, ';'); semi >= 0 {
			key = key[:semi]
		}
		if !strings.EqualFold(key, "ATTENDEE") {
			continue
		}
		// Parameters sit between the property name and the colon.
		semiIdx := strings.IndexByte(line, ';')
		params := line[len("ATTENDEE"):semiIdx]
		if i := strings.LastIndex(strings.ToLower(params), "partstat="); i >= 0 {
			rest := params[i+len("partstat="):]
			if semi := strings.IndexByte(rest, ';'); semi >= 0 {
				rest = rest[:semi]
			}
			partstat = strings.TrimSpace(rest)
		}
		// The address is the mailto: value, or the CN parameter's value.
		if i := strings.LastIndex(strings.ToLower(val), "mailto:"); i >= 0 {
			addr = strings.TrimSpace(val[i+len("mailto:"):])
		} else if val != "" {
			addr = strings.TrimSpace(val)
		}
		return addr, partstat
	}
	return "", ""
}

// partstatResponse maps an iCalendar PARTSTAT to PidLidResponseStatus; an unknown
// value maps to 0 (no update).
func partstatResponse(partstat string) int32 {
	switch strings.ToUpper(strings.TrimSpace(partstat)) {
	case "ACCEPTED":
		return ResponseAccepted
	case "TENTATIVE":
		return ResponseTentative
	case "DECLINED":
		return ResponseDeclined
	}
	return 0
}
