package dav

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// CalDAV scheduling (RFC 6638). The scheduling Outbox accepts a POST carrying an
// iTIP message; this increment serves the free-busy request (METHOD:REQUEST with a
// VFREEBUSY): for each ATTENDEE that resolves to a local mailbox the server computes
// that user's busy periods and returns them in a CALDAV:schedule-response. hermEX
// does not perform server-to-server (iSchedule) lookups, so a non-local attendee is
// reported as an invalid calendar user rather than queried remotely.

// handleOutboxPost answers a POST to the scheduling Outbox (RFC 6638 §5).
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

	// This increment handles free-busy requests; event scheduling via the Outbox is
	// delivered in a later increment.
	var vfb *icalNode
	for _, c := range root.kids {
		if strings.EqualFold(c.name, "VFREEBUSY") {
			vfb = c
			break
		}
	}
	if vfb == nil {
		http.Error(w, "only free-busy requests are supported on the scheduling Outbox", http.StatusNotImplemented)
		return
	}

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
