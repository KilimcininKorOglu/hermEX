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
// sender is the delivered message's envelope sender. The ATTENDEE line is body
// content, so it says only who the message CLAIMS to answer for: without this
// check any invitee who knows the meeting UID could set a co-invitee's tracking
// status by mailing the organizer. A REPLY is therefore honoured only for the
// attendee who actually sent it. That is deliberately strict: a reply relayed by
// anyone else (a delegate answering for an attendee) updates nothing, because
// nothing in the message proves the attendee authorized it.
//
// It reports whether a REPLY was handled, plus the error from the one step that
// actually changes state (writing the attendee's response status). Both matter:
// the tracking update can fail on its own while the message is delivered fine, and
// then the organizer's Tracking tab shows the attendee as never having answered.
// The caller logs the error and carries on.
//
// It is best-effort and never errors a delivery: a malformed, unmatched or
// unauthorized REPLY is left as an ordinary email the organizer can read, not a
// delivery failure.
func ProcessReply(st *objectstore.Store, sender string, messageID int64) (bool, error) {
	// The delivery pass hands an object-store message id; the raw read is keyed by
	// IMAP UID, and the two diverge as soon as a mailbox holds any non-mail object
	// (a calendar item consumes an id but no UID). Resolving one to the other is
	// what makes tracking work in a mailbox that has ever held an appointment.
	uidOf, ok, err := st.MessageUIDByID(int64(mapi.PrivateFIDInbox), messageID)
	if err != nil || !ok {
		return false, nil
	}
	raw, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), uidOf)
	if err != nil {
		return false, nil
	}
	ics := findCalendarPart(mime.ParseStructure(raw))
	if ics == nil {
		return false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(icalLine(ics, "METHOD")), "REPLY") {
		return false, nil
	}
	uid := strings.TrimSpace(icalLine(ics, "UID"))
	attendee, partstat := parseAttendee(ics)
	if uid == "" || attendee == "" {
		return false, nil
	}
	// An empty envelope sender (a bounce, or a locally injected message that
	// carries none) proves nothing either, so it updates no tracking.
	from := strings.ToLower(strings.TrimSpace(sender))
	if from == "" || from != strings.ToLower(strings.TrimSpace(attendee)) {
		return false, nil
	}
	tags, err := ResolveTags(st)
	if err != nil {
		return false, nil
	}
	resp := partstatResponse(partstat)
	if resp == 0 {
		return false, nil
	}
	// Report the failure rather than swallowing it: the REPLY was understood and
	// authorized, so "handled" is true, but the tracking write is the whole point
	// and losing it silently leaves the organizer with a stale Tracking tab.
	return true, ApplyReply(st, tags, uid, attendee, resp)
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
		// Everything before the colon is the property name and, after the first
		// semicolon, its parameters. Splitting here rather than re-scanning the whole
		// line matters twice: the first semicolon of the line marks where parameters
		// BEGIN, not where they end, and a parameterless ATTENDEE has no semicolon at
		// all, so an offset computed from one is negative.
		name, params, _ := strings.Cut(key, ";")
		if !strings.EqualFold(name, "ATTENDEE") {
			continue
		}
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
