package dav

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// CalDAV scheduling (RFC 6638). The scheduling Outbox accepts a POST carrying an
// iTIP message; this increment serves the free-busy request (METHOD:REQUEST with a
// VFREEBUSY): for each ATTENDEE that resolves to a local mailbox the server computes
// that user's busy periods and returns them in a CALDAV:schedule-response. hermEX
// does not perform server-to-server (iSchedule) lookups, so a non-local attendee is
// reported as an invalid calendar user rather than queried remotely.

// handleOutboxPost answers a POST to the scheduling Outbox (RFC 6638 §5): a
// VFREEBUSY request is answered with each attendee's busy periods, while an event
// component (VEVENT) carrying an iTIP METHOD is delivered to its recipients.
func (s *Server) handleOutboxPost(w http.ResponseWriter, r *http.Request, user string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, s.icalLimit()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	root := parseICalNode(string(body))
	if root == nil || !strings.EqualFold(root.name, "VCALENDAR") {
		schedulePreconditionFail(w, "valid-scheduling-message", http.StatusBadRequest)
		return
	}
	for _, c := range root.kids {
		switch strings.ToUpper(c.name) {
		case "VFREEBUSY":
			s.outboxFreeBusy(w, user, c)
			return
		case "VEVENT", "VTODO":
			s.outboxDeliver(w, user, root, c, string(body))
			return
		}
	}
	http.Error(w, "unsupported scheduling message", http.StatusNotImplemented)
}

// outboxFreeBusy answers a free-busy request: for each ATTENDEE that resolves to a
// local mailbox it returns that user's busy periods (RFC 6638 §5).
func (s *Server) outboxFreeBusy(w http.ResponseWriter, user string, vfb *icalNode) {
	// valid-organizer (RFC 6638 §5.2.6): the ORGANIZER must be a calendar user
	// address of the Outbox owner.
	if !addrMatchesOwner(user, firstPropValue(vfb, "ORGANIZER")) {
		schedulePreconditionFail(w, "valid-organizer", http.StatusForbidden)
		return
	}

	// The query window is the request VFREEBUSY's DTSTART/DTEND; an absent bound
	// leaves that side open.
	rangeStart, okS := propTimeValue(vfb, "DTSTART")
	rangeEnd, okE := propTimeValue(vfb, "DTEND")

	resp := &scheduleResponse{}
	for _, att := range vfb.propsByName("ATTENDEE") {
		recipient := strings.TrimSpace(att.value)
		path, ok := s.accounts.Resolve(stripMailto(recipient))
		if !ok {
			resp.Responses = append(resp.Responses, invalidRecipient(recipient))
			continue
		}
		data, err := attendeeFreeBusy(path, user, recipient, rangeStart, rangeEnd, okS, okE)
		if err != nil {
			resp.Responses = append(resp.Responses, invalidRecipient(recipient))
			continue
		}
		resp.Responses = append(resp.Responses, scheduleRespItem{
			Recipient:     href{Href: recipient},
			RequestStatus: "2.0;Success",
			CalendarData:  data,
		})
	}
	writeScheduleResponse(w, resp)
}

// outboxDeliver delivers an iTIP scheduling message (REQUEST/CANCEL from the
// organizer, REPLY from an attendee) to its recipients through the shared mail
// submission path, then reports per-recipient status (RFC 6638 §5.2). The message
// is wrapped as iMIP via oxcmail.Export — the one proven outgoing-mail path — and
// handed to mta.DeliverAndRelay, which files it in each local recipient's mailbox
// and relays external ones when a spool is configured.
func (s *Server) outboxDeliver(w http.ResponseWriter, user string, root, comp *icalNode, body string) {
	method := strings.ToUpper(firstPropValue(root, "METHOD"))
	organizer := firstPropValue(comp, "ORGANIZER")
	attendees := allPropValues(comp, "ATTENDEE")

	// An attendee-originated method (a reply) is addressed to the organizer; an
	// organizer-originated one (a request/cancel) to the attendees.
	var recipients []string
	switch method {
	case "REPLY", "REFRESH", "COUNTER":
		// The Outbox owner must be one of the attendees on whose behalf the reply is
		// sent, and the organizer is the sole recipient.
		if !ownerAmong(user, attendees) || organizer == "" {
			schedulePreconditionFail(w, "valid-organizer", http.StatusForbidden)
			return
		}
		recipients = []string{organizer}
	default:
		// valid-organizer (RFC 6638 §5.2.6): the ORGANIZER must be the Outbox owner.
		if !addrMatchesOwner(user, organizer) {
			schedulePreconditionFail(w, "valid-organizer", http.StatusForbidden)
			return
		}
		recipients = attendees
	}
	if len(recipients) == 0 || method == "" {
		schedulePreconditionFail(w, "valid-scheduling-message", http.StatusBadRequest)
		return
	}

	raw, err := buildITIP(user, recipients, scheduleSubject(method, firstPropValue(comp, "SUMMARY")), body, method)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	unresolved, err := mta.DeliverAndRelay(s.accounts, s.spool, user, stripMailtoAll(recipients), raw, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	bad := make(map[string]bool, len(unresolved))
	for _, u := range unresolved {
		bad[strings.ToLower(u)] = true
	}

	resp := &scheduleResponse{}
	for _, rcpt := range recipients {
		if bad[strings.ToLower(stripMailto(rcpt))] {
			resp.Responses = append(resp.Responses, invalidRecipient(rcpt))
			continue
		}
		resp.Responses = append(resp.Responses, scheduleRespItem{
			Recipient:     href{Href: rcpt},
			RequestStatus: "2.0;Success",
		})
	}
	writeScheduleResponse(w, resp)
}

// buildITIP wraps an iTIP scheduling message as an iMIP email (RFC 6047) addressed
// from the originator to the recipients, carrying the iCalendar as a text/calendar
// part with its METHOD. The MIME is produced by oxcmail.Export, never hand-rolled.
func buildITIP(originator string, recipients []string, subject, body, method string) ([]byte, error) {
	msg := &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: subject},
		{Tag: mapi.PrSenderSmtpAddress, Value: originator},
		{Tag: mapi.PrSenderEmailAddress, Value: originator},
		{Tag: mapi.PrSenderAddrType, Value: "SMTP"},
	}}
	for _, rcpt := range recipients {
		msg.Recipients = append(msg.Recipients, mapi.PropertyValues{
			{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
			{Tag: mapi.PrSmtpAddress, Value: stripMailto(rcpt)},
		})
	}
	oxcmail.EnsureMessageID(&msg.Props)
	return oxcmail.Export(msg, oxcmail.Options{CalendarBody: []byte(body), CalendarMethod: method})
}

// allPropValues returns the values of every property of the given name in a
// component (e.g. the full ATTENDEE list).
func allPropValues(n *icalNode, name string) []string {
	var out []string
	for _, p := range n.propsByName(name) {
		out = append(out, strings.TrimSpace(p.value))
	}
	return out
}

// ownerAmong reports whether the Outbox owner is one of the listed calendar
// addresses.
func ownerAmong(user string, addrs []string) bool {
	for _, a := range addrs {
		if addrMatchesOwner(user, a) {
			return true
		}
	}
	return false
}

// stripMailtoAll strips the mailto: scheme from each address for directory
// resolution and relay routing.
func stripMailtoAll(addrs []string) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = stripMailto(a)
	}
	return out
}

// scheduleSubject builds a human-readable iMIP subject from the iTIP method and the
// event summary.
func scheduleSubject(method, summary string) string {
	if summary == "" {
		summary = "Meeting"
	}
	switch method {
	case "CANCEL":
		return "Canceled: " + summary
	case "REPLY":
		return "Response: " + summary
	default:
		return summary
	}
}

// invalidRecipient marks a recipient that does not resolve to a local mailbox; a
// remote address would require an iSchedule lookup hermEX does not perform.
func invalidRecipient(recipient string) scheduleRespItem {
	return scheduleRespItem{
		Recipient:     href{Href: recipient},
		RequestStatus: "3.7;Invalid calendar user",
	}
}

// attendeeFreeBusy computes one local attendee's busy periods over the requested
// window and renders them as an iTIP free-busy reply (RFC 5546 §3.3.2).
func attendeeFreeBusy(mailboxPath, organizer, recipient string, rangeStart, rangeEnd time.Time, okS, okE bool) (string, error) {
	st, err := objectstore.Open(mailboxPath)
	if err != nil {
		return "", err
	}
	defer st.Close()
	periods, err := busyPeriods(st, int64(mapi.PrivateFIDCalendar), rangeStart, rangeEnd, okS, okE)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//CalDAV//EN\r\nMETHOD:REPLY\r\nBEGIN:VFREEBUSY\r\n")
	if okS {
		fmt.Fprintf(&b, "DTSTART:%s\r\n", formatICalUTCZ(rangeStart))
	}
	if okE {
		fmt.Fprintf(&b, "DTEND:%s\r\n", formatICalUTCZ(rangeEnd))
	}
	fmt.Fprintf(&b, "ORGANIZER:mailto:%s\r\n", organizer)
	fmt.Fprintf(&b, "ATTENDEE:%s\r\n", recipient)
	fmt.Fprintf(&b, "DTSTAMP:%s\r\n", formatICalUTCZ(time.Now()))
	for _, p := range periods {
		fmt.Fprintf(&b, "FREEBUSY;FBTYPE=BUSY:%s\r\n", p)
	}
	b.WriteString("END:VFREEBUSY\r\nEND:VCALENDAR\r\n")
	return b.String(), nil
}

// busyPeriods aggregates the BUSY (non-transparent) VEVENT spans in a calendar
// folder that overlap [rangeStart,rangeEnd), each clamped to the range, as
// iCalendar PERIOD strings for a VFREEBUSY (RFC 4791 §7.10). okS/okE mark which
// bounds are set; an unset bound leaves that side open.
func busyPeriods(st *objectstore.Store, fid int64, rangeStart, rangeEnd time.Time, okS, okE bool) ([]string, error) {
	objs, err := st.ListFolderObjects(fid)
	if err != nil {
		return nil, err
	}
	var periods []string
	for _, o := range objs {
		data, err := calendarData(st, o.ID)
		if err != nil {
			continue
		}
		root := parseICalNode(data)
		if root == nil {
			continue
		}
		for _, ev := range root.kids {
			if !strings.EqualFold(ev.name, "VEVENT") {
				continue
			}
			// A transparent event does not block time (RFC 4791 §7.10).
			if tp := ev.propsByName("TRANSP"); len(tp) > 0 && strings.EqualFold(strings.TrimSpace(tp[0].value), "TRANSPARENT") {
				continue
			}
			start, end, ok := eventSpan(ev)
			if !ok {
				continue
			}
			if (okE && !start.Before(rangeEnd)) || (okS && !end.After(rangeStart)) {
				continue // outside the requested range
			}
			cs, ce := start, end
			if okS && cs.Before(rangeStart) {
				cs = rangeStart
			}
			if okE && ce.After(rangeEnd) {
				ce = rangeEnd
			}
			periods = append(periods, formatICalUTCZ(cs)+"/"+formatICalUTCZ(ce))
		}
	}
	return periods, nil
}

// firstPropValue returns the value of a component's first property of the given
// name, or "" when absent.
func firstPropValue(n *icalNode, name string) string {
	if ps := n.propsByName(name); len(ps) > 0 {
		return strings.TrimSpace(ps[0].value)
	}
	return ""
}

// propTimeValue parses a component's first property of the given name as a UTC
// instant.
func propTimeValue(n *icalNode, name string) (time.Time, bool) {
	ps := n.propsByName(name)
	if len(ps) == 0 {
		return time.Time{}, false
	}
	t, _, ok := propTime(ps[0])
	return t, ok
}

// addrMatchesOwner reports whether a calendar user address identifies the Outbox
// owner: their mailto: address or principal URL.
func addrMatchesOwner(user, val string) bool {
	val = strings.TrimSpace(val)
	return strings.EqualFold(val, "mailto:"+user) || val == principalPath(user)
}

// stripMailto removes a leading "mailto:" scheme (any case) from a calendar user
// address, leaving the bare email for directory resolution.
func stripMailto(uri string) string {
	uri = strings.TrimSpace(uri)
	if len(uri) >= 7 && strings.EqualFold(uri[:7], "mailto:") {
		return uri[7:]
	}
	return uri
}

// schedulePreconditionFail writes a DAV:error carrying the named CalDAV
// precondition at the given status (RFC 6638 §5.2).
func schedulePreconditionFail(w http.ResponseWriter, precondition string, status int) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "%s<D:error xmlns:D=\"DAV:\" xmlns:C=\"urn:ietf:params:xml:ns:caldav\"><C:%s/></D:error>\n", xml.Header, precondition)
}

// writeScheduleResponse serializes a CALDAV:schedule-response (RFC 6638 §5.2).
func writeScheduleResponse(w http.ResponseWriter, resp *scheduleResponse) {
	body, err := xml.Marshal(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append([]byte(xml.Header), body...))
}
