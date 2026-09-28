package meeting

import (
	"strings"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/sendas"
)

// ProcessReply processes an inbound iTIP REPLY or COUNTER on the organizer's side:
// it reads the delivered message, extracts the text/calendar part, parses the
// responder's PARTSTAT and updates that attendee's PidLidResponseStatus on the
// organizer's calendar event (matched by the iCalendar UID). A COUNTER also records
// the time the attendee proposes. It reports whether a response was handled.
//
// sender is the delivered message's envelope sender. The ATTENDEE line is body
// content, so it says only who the message CLAIMS to answer for: without this
// check any invitee who knows the meeting UID could set a co-invitee's tracking
// status by mailing the organizer. A REPLY is therefore honoured for the attendee
// who sent it, and for a delegate whose right to send for the attendee accounts
// records. A reply relayed by anyone else updates nothing, because nothing in the
// message proves the attendee authorized it.
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
func ProcessReply(st *objectstore.Store, accounts directory.Accounts, sender string, messageID int64) (bool, error) {
	ics, ok := inboxCalendarPart(st, messageID)
	if !ok {
		return false, nil
	}
	fallback, ok := responseMethods[strings.ToUpper(strings.TrimSpace(icalLine(ics, "METHOD")))]
	if !ok {
		return false, nil
	}
	uid, attendee, resp, ok := authorizedReply(ics, accounts, sender, fallback)
	if !ok {
		return false, nil
	}
	tags, err := ResolveTags(st)
	if err != nil {
		return false, nil
	}
	// Report the failure rather than swallowing it: the REPLY was understood and
	// authorized, so "handled" is true, but the tracking write is the whole point
	// and losing it silently leaves the organizer with a stale Tracking tab.
	proposal, err := readProposal(st, messageID)
	if err != nil {
		return true, err
	}
	return true, ApplyReply(st, tags, uid, attendee, resp, proposal)
}

// responseMethods are the iTIP methods an attendee answers with, each with the
// response status that stands when the ATTENDEE carries no PARTSTAT. A COUNTER is a
// tentative response proposing a new time ([MS-OXCICAL] METHOD table), and RFC 5546
// §3.2.7 does not require it to name a PARTSTAT; a REPLY without one says nothing.
var responseMethods = map[string]int32{
	"REPLY":   0,
	"COUNTER": ResponseTentative,
}

// inboxCalendarPart reads the delivered message and returns its calendar body.
func inboxCalendarPart(st *objectstore.Store, messageID int64) ([]byte, bool) {
	// The delivery pass hands an object-store message id; the raw read is keyed by
	// IMAP UID, and the two diverge as soon as a mailbox holds any non-mail object
	// (a calendar item consumes an id but no UID). Resolving one to the other is
	// what makes tracking work in a mailbox that has ever held an appointment.
	uidOf, ok, err := st.MessageUIDByID(int64(mapi.PrivateFIDInbox), messageID)
	if err != nil || !ok {
		return nil, false
	}
	raw, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), uidOf)
	if err != nil {
		return nil, false
	}
	ics := findCalendarPart(mime.ParseStructure(raw))
	return ics, ics != nil
}

// authorizedReply reads the REPLY's UID, attendee and response status. It reports ok
// only when the envelope sender may answer for the attendee the body names, because
// the ATTENDEE line alone says who the message claims to answer for. fallback is
// the response that stands when the ATTENDEE names no PARTSTAT (0: none).
func authorizedReply(ics []byte, accounts directory.Accounts, sender string, fallback int32) (uid, attendee string, resp int32, ok bool) {
	uid = strings.TrimSpace(icalLine(ics, "UID"))
	a := parseAttendee(ics)
	if uid == "" || a.addr == "" || !mayAnswerFor(accounts, sender, a) {
		return "", "", 0, false
	}
	resp = partstatResponse(a.partstat)
	if resp == 0 {
		resp = fallback
	}
	if resp == 0 {
		return "", "", 0, false
	}
	return uid, a.addr, resp, true
}

// mayAnswerFor reports whether the envelope sender may answer for the attendee: it
// is the attendee, or this directory records its right to send for the attendee (an
// alias of its own, or a send-as or on-behalf grant), the right a delegate answering
// in the attendee's mailbox holds. A delegate shows itself in one of two ways: in
// the attendee's SENT-BY (RFC 5546 section 3.2.3), or in the message's Sender only,
// which is how Exchange sends one, as it writes no SENT-BY ([MS-STANXICAL]). A
// SENT-BY that names someone other than the envelope sender contradicts it, so such
// a reply is refused. An empty envelope sender (a bounce, or a locally injected
// message that carries none) proves nothing, and neither does a delegate of an
// attendee this server does not hold, whose grants it cannot read.
func mayAnswerFor(accounts directory.Accounts, sender string, a replyAttendee) bool {
	from := strings.TrimSpace(sender)
	switch {
	case from == "":
		return false
	case strings.EqualFold(from, a.addr):
		return true
	case a.sentBy != "" && !strings.EqualFold(from, a.sentBy), accounts == nil:
		return false
	}
	return sendas.Allows(accounts, from, a.addr)
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

// icalLine returns the value of the first property named name in the iCalendar
// stream, or "". It is a scanner sufficient for REPLY's METHOD/UID, not a general
// parser, but it reads content lines the way RFC 5545 section 3.1 defines them:
// unfolded, and split at the first colon outside a quoted parameter.
func icalLine(ics []byte, name string) string {
	for _, line := range oxcical.ContentLines(ics) {
		key, _, val := oxcical.SplitContentLine(line)
		if strings.EqualFold(key, name) {
			return val
		}
	}
	return ""
}

// replyAttendee is the one attendee an iTIP response answers for.
type replyAttendee struct {
	// addr is the attendee's address, partstat its participation status.
	addr, partstat string
	// sentBy is the address that submitted the answer for the attendee, a delegate,
	// from the SENT-BY parameter (RFC 5545 section 3.2.18); "" when the attendee
	// answered itself.
	sentBy string
}

// parseAttendee reads the ATTENDEE line: the responder's address (the mailto:
// value), its PARTSTAT and its SENT-BY. The REPLY carries a single attendee.
func parseAttendee(ics []byte) replyAttendee {
	for _, line := range oxcical.ContentLines(ics) {
		name, params, val := oxcical.SplitContentLine(line)
		if !strings.EqualFold(name, "ATTENDEE") {
			continue
		}
		a := replyAttendee{addr: calAddress(val)}
		if v := params["PARTSTAT"]; len(v) > 0 {
			a.partstat = strings.TrimSpace(v[0])
		}
		if v := params["SENT-BY"]; len(v) > 0 {
			a.sentBy = calAddress(v[0])
		}
		return a
	}
	return replyAttendee{}
}

// calAddress reads the address out of a cal-address, the mailto: URI an ATTENDEE
// value or a SENT-BY parameter carries; a value without the scheme is taken whole.
func calAddress(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.LastIndex(strings.ToLower(v), "mailto:"); i >= 0 {
		v = v[i+len("mailto:"):]
	}
	return strings.TrimSpace(v)
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
