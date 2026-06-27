package dav

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mta"
)

// CalDAV implicit scheduling (RFC 6638 §3). When a calendar user PUT/DELETEs an
// event in their own calendar, the server detects the organizer/attendee state and
// auto-delivers the resulting iTIP messages, rather than requiring the client to
// POST them to the scheduling Outbox (the legacy model in schedule.go). The presence
// of this behavior is advertised by the calendar-auto-schedule DAV class in
// handleOptions; without that advertisement a conformant client would ALSO POST the
// iTIP to its Outbox and the invite would be delivered twice.

// significantChangeProps are the event properties whose change makes an update worth
// re-sending to already-invited attendees (RFC 5546 §2.1.4). An edit touching none of
// these (e.g. only SUMMARY/DESCRIPTION) may be withheld from retained attendees; a
// newly added attendee is always invited regardless.
var significantChangeProps = []string{
	"DTSTART", "DTEND", "DURATION", "DUE", "RRULE", "RDATE", "EXDATE", "STATUS",
}

// itipMsg is one scheduling message to deliver: an iTIP METHOD, the recipient
// calendar user addresses, the iCalendar body carrying that METHOD, and the event
// summary used to build the iMIP subject.
type itipMsg struct {
	method     string
	recipients []string
	body       string
	summary    string
}

// scheduleOnChange runs the implicit-scheduling broker for a calendar object change
// by owner and delivers the resulting iTIP messages best-effort. oldBody is the prior
// stored iCalendar ("" on create), newBody the new one ("" on delete). Delivery
// failures never propagate: the calendar write has already committed, and per RFC
// 6638 the calendar store (not the HTTP status) is the source of truth for
// scheduling state. Local recipients are filed into their mailbox (rendered as a
// meeting by the MAPI-family clients); external recipients relay when a spool is set.
// suppressReply drops the attendee REPLY (RFC 6638 8.1, Schedule-Reply:F on delete).
func (s *Server) scheduleOnChange(owner, oldBody, newBody string, suppressReply bool) {
	for _, m := range schedulingMessages(owner, oldBody, newBody) {
		if suppressReply && m.method == "REPLY" {
			continue
		}
		raw, err := buildITIP(owner, m.recipients, scheduleSubject(m.method, m.summary), m.body, m.method)
		if err != nil {
			s.Logger.Emit(logging.Event{Level: logging.LevelWarn, Subsystem: logging.DAV, Name: "schedule.build.error", User: owner,
				Fields: logging.Fields{"method": m.method, "recipients": len(m.recipients)}, Err: err.Error()})
			continue
		}
		unresolved, derr := mta.DeliverAndRelay(s.accounts, s.spool, owner, stripMailtoAll(m.recipients), raw, time.Now())
		if derr != nil {
			s.Logger.Emit(logging.Event{Level: logging.LevelWarn, Subsystem: logging.DAV, Name: "schedule.deliver.error", User: owner,
				Fields: logging.Fields{"method": m.method, "recipients": len(m.recipients)}, Err: derr.Error()})
			continue
		}
		if len(unresolved) > 0 {
			s.Logger.Emit(logging.Event{Level: logging.LevelWarn, Subsystem: logging.DAV, Name: "schedule.deliver.unresolved", User: owner,
				Fields: logging.Fields{"method": m.method, "unresolved": strings.Join(unresolved, ",")}})
		}
	}
}

// schedulingMessages computes the iTIP messages implied by owner's calendar object
// change (RFC 6638 §3). It returns organizer-side REQUEST/CANCEL when owner is the
// ORGANIZER, or an attendee-side REPLY when owner is an ATTENDEE; an empty result
// means there is nothing to schedule (no organizer, or no attendees on either side).
func schedulingMessages(owner, oldBody, newBody string) []itipMsg {
	oldEv := firstVEvent(oldBody)
	newEv := firstVEvent(newBody)
	if oldEv == nil && newEv == nil {
		return nil
	}
	organizer := nodeOrganizer(newEv)
	if organizer == "" {
		organizer = nodeOrganizer(oldEv)
	}
	if organizer == "" {
		return nil // not a scheduling object: no ORGANIZER, no messages
	}
	if addrMatchesOwner(owner, organizer) {
		return organizerMessages(oldEv, newEv, newBody, organizer)
	}
	return attendeeMessages(owner, organizer, oldEv, newEv)
}

// organizerMessages builds the REQUEST/CANCEL messages an organizer's change implies:
// REQUEST to newly added attendees (always) and retained attendees (only on a
// significant change), CANCEL to removed attendees, and (on deletion) CANCEL to all.
func organizerMessages(oldEv, newEv *icalNode, newBody, organizer string) []itipMsg {
	newAtt := schedulableAttendees(newEv, organizer)
	oldAtt := schedulableAttendees(oldEv, organizer)
	if len(newAtt) == 0 && len(oldAtt) == 0 {
		return nil
	}
	summary := eventSummary(newEv, oldEv)
	var out []itipMsg

	if newEv != nil {
		significant := oldEv == nil || significantlyChanged(oldEv, newEv)
		var requestTo []string
		for key, att := range newAtt {
			if _, retained := oldAtt[key]; !retained || significant {
				requestTo = append(requestTo, strings.TrimSpace(att.value))
			}
		}
		if len(requestTo) > 0 {
			out = append(out, itipMsg{method: "REQUEST", recipients: requestTo, body: withMethod(newBody, "REQUEST"), summary: summary})
		}
		var cancelTo []string
		for key, att := range oldAtt {
			if _, kept := newAtt[key]; !kept {
				cancelTo = append(cancelTo, strings.TrimSpace(att.value))
			}
		}
		if len(cancelTo) > 0 {
			out = append(out, itipMsg{method: "CANCEL", recipients: cancelTo, body: cancelBody(oldEv, organizer, cancelTo), summary: summary})
		}
		return out
	}

	// Deletion by the organizer cancels the meeting for every attendee.
	var cancelTo []string
	for _, att := range oldAtt {
		cancelTo = append(cancelTo, strings.TrimSpace(att.value))
	}
	if len(cancelTo) > 0 {
		out = append(out, itipMsg{method: "CANCEL", recipients: cancelTo, body: cancelBody(oldEv, organizer, cancelTo), summary: summary})
	}
	return out
}

// attendeeMessages builds the REPLY an attendee's change implies: when owner's own
// PARTSTAT changed (or owner deleted the invite, which declines it), a REPLY goes to
// the organizer. An attendee whose SCHEDULE-AGENT is CLIENT is left to its client.
func attendeeMessages(owner, organizer string, oldEv, newEv *icalNode) []itipMsg {
	ownerKey := normalizeCalAddr(owner)

	if newEv != nil {
		att, ok := ownerAttendeeProp(newEv, ownerKey)
		if !ok {
			return nil // owner is not an attendee on the new event
		}
		if strings.EqualFold(att.param("SCHEDULE-AGENT"), "CLIENT") {
			return nil
		}
		newPart := attendeePartstat(att)
		oldPart := ""
		if prev, had := ownerAttendeeProp(oldEv, ownerKey); had {
			oldPart = attendeePartstat(prev)
		}
		if newPart == oldPart {
			return nil // PARTSTAT unchanged: nothing to reply
		}
		return []itipMsg{replyMessage(newEv, oldEv, organizer, owner, newPart)}
	}

	// Deletion of the invite from one's own calendar declines it.
	if _, ok := ownerAttendeeProp(oldEv, ownerKey); !ok {
		return nil
	}
	return []itipMsg{replyMessage(nil, oldEv, organizer, owner, "DECLINED")}
}

// replyMessage builds an attendee's METHOD:REPLY to the organizer carrying the
// owner's ATTENDEE line with its new PARTSTAT.
func replyMessage(newEv, oldEv *icalNode, organizer, owner, partstat string) itipMsg {
	uid := nodeProp(newEv, "UID")
	if uid == "" {
		uid = nodeProp(oldEv, "UID")
	}
	seq := sequenceOf(newEv)
	if newEv == nil {
		seq = sequenceOf(oldEv)
	}
	summary := eventSummary(newEv, oldEv)

	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//CalDAV//EN\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\n")
	fmt.Fprintf(&b, "UID:%s\r\n", uid)
	fmt.Fprintf(&b, "DTSTAMP:%s\r\n", formatICalUTCZ(time.Now()))
	fmt.Fprintf(&b, "SEQUENCE:%d\r\n", seq)
	fmt.Fprintf(&b, "ORGANIZER:%s\r\n", organizer)
	fmt.Fprintf(&b, "ATTENDEE;PARTSTAT=%s:mailto:%s\r\n", partstat, stripMailto(owner))
	if summary != "" {
		fmt.Fprintf(&b, "SUMMARY:%s\r\n", summary)
	}
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return itipMsg{method: "REPLY", recipients: []string{organizer}, body: b.String(), summary: summary}
}

// cancelBody builds a METHOD:CANCEL iCalendar for the given event addressed to the
// cancelled attendees, with STATUS:CANCELLED and a bumped SEQUENCE (RFC 5546 §3.2.5).
func cancelBody(ev *icalNode, organizer string, cancelTo []string) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//CalDAV//EN\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\n")
	fmt.Fprintf(&b, "UID:%s\r\n", nodeProp(ev, "UID"))
	fmt.Fprintf(&b, "DTSTAMP:%s\r\n", formatICalUTCZ(time.Now()))
	fmt.Fprintf(&b, "SEQUENCE:%d\r\n", sequenceOf(ev)+1)
	fmt.Fprintf(&b, "ORGANIZER:%s\r\n", organizer)
	if ds := ev.propsByName("DTSTART"); len(ds) > 0 {
		fmt.Fprintf(&b, "%s\r\n", renderProp(ds[0]))
	}
	if summary := nodeProp(ev, "SUMMARY"); summary != "" {
		fmt.Fprintf(&b, "SUMMARY:%s\r\n", summary)
	}
	for _, addr := range cancelTo {
		fmt.Fprintf(&b, "ATTENDEE:%s\r\n", addr)
	}
	b.WriteString("STATUS:CANCELLED\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String()
}

// withMethod returns the iCalendar body with its VCALENDAR METHOD set to method,
// replacing any existing METHOD line. The full event (every component, property, and
// parameter) is preserved so an invite carries exactly what the organizer authored.
func withMethod(body, method string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines)+1)
	inserted := false
	for _, ln := range lines {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(ln)), "METHOD:") {
			continue // drop any existing METHOD; the server sets its own
		}
		out = append(out, ln)
		if !inserted && strings.EqualFold(strings.TrimSpace(ln), "BEGIN:VCALENDAR") {
			out = append(out, "METHOD:"+method)
			inserted = true
		}
	}
	return strings.Join(out, "\r\n")
}

// firstVEvent returns the first VEVENT component of an iCalendar body, or nil when
// the body is empty or carries no VEVENT.
func firstVEvent(body string) *icalNode {
	if body == "" {
		return nil
	}
	root := parseICalNode(body)
	if root == nil {
		return nil
	}
	for _, k := range root.kids {
		if strings.EqualFold(k.name, "VEVENT") {
			return k
		}
	}
	return nil
}

// nodeOrganizer returns the ORGANIZER value of an event, or "" when absent.
func nodeOrganizer(ev *icalNode) string {
	if ev == nil {
		return ""
	}
	return firstPropValue(ev, "ORGANIZER")
}

// nodeProp returns the first value of the named property, nil-safe.
func nodeProp(ev *icalNode, name string) string {
	if ev == nil {
		return ""
	}
	return firstPropValue(ev, name)
}

// schedulableAttendees returns the event's ATTENDEEs keyed by normalized address,
// excluding the organizer and any attendee whose SCHEDULE-AGENT is CLIENT (RFC 6638
// §7.1: CLIENT means the client performs scheduling, so the server must not).
func schedulableAttendees(ev *icalNode, organizer string) map[string]icalProp {
	out := map[string]icalProp{}
	if ev == nil {
		return out
	}
	orgKey := normalizeCalAddr(organizer)
	for _, p := range ev.propsByName("ATTENDEE") {
		if strings.EqualFold(p.param("SCHEDULE-AGENT"), "CLIENT") {
			continue
		}
		key := normalizeCalAddr(p.value)
		if key == "" || key == orgKey {
			continue
		}
		if _, dup := out[key]; !dup {
			out[key] = p
		}
	}
	return out
}

// ownerAttendeeProp finds owner's own ATTENDEE property on an event.
func ownerAttendeeProp(ev *icalNode, ownerKey string) (icalProp, bool) {
	if ev == nil {
		return icalProp{}, false
	}
	for _, p := range ev.propsByName("ATTENDEE") {
		if normalizeCalAddr(p.value) == ownerKey {
			return p, true
		}
	}
	return icalProp{}, false
}

// attendeePartstat returns an attendee's PARTSTAT, defaulting to NEEDS-ACTION.
func attendeePartstat(p icalProp) string {
	if v := strings.ToUpper(p.param("PARTSTAT")); v != "" {
		return v
	}
	return "NEEDS-ACTION"
}

// significantlyChanged reports whether any RFC 5546 §2.1.4 significant property
// differs between the old and new event.
func significantlyChanged(oldEv, newEv *icalNode) bool {
	for _, name := range significantChangeProps {
		if joinPropValues(oldEv, name) != joinPropValues(newEv, name) {
			return true
		}
	}
	return false
}

// joinPropValues returns the event's values for a property name, sorted and joined,
// so multi-valued properties (RDATE/EXDATE) compare order-independently.
func joinPropValues(ev *icalNode, name string) string {
	if ev == nil {
		return ""
	}
	var vals []string
	for _, p := range ev.propsByName(name) {
		vals = append(vals, strings.TrimSpace(p.value))
	}
	sort.Strings(vals)
	return strings.Join(vals, ",")
}

// eventSummary returns the SUMMARY of the first non-nil event that has one.
func eventSummary(evs ...*icalNode) string {
	for _, ev := range evs {
		if s := nodeProp(ev, "SUMMARY"); s != "" {
			return s
		}
	}
	return ""
}

// sequenceOf returns an event's SEQUENCE value (0 when absent or unparseable).
func sequenceOf(ev *icalNode) int {
	if v := nodeProp(ev, "SEQUENCE"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return 0
}

// renderProp serializes a parsed property back to a content line, carrying its
// parameters (e.g. DTSTART;TZID=Europe/Istanbul:...).
func renderProp(p icalProp) string {
	var b strings.Builder
	b.WriteString(p.name)
	for key, vals := range p.params {
		for _, v := range vals {
			fmt.Fprintf(&b, ";%s=%s", key, v)
		}
	}
	b.WriteString(":")
	b.WriteString(p.value)
	return b.String()
}

// normalizeCalAddr reduces a calendar user address to a comparison key (lowercased,
// mailto: stripped).
func normalizeCalAddr(v string) string {
	return strings.ToLower(stripMailto(v))
}
