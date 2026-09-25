package webmail2api

import (
	"errors"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"hermex/internal/itip"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// asfReceived is the PidLidAppointmentStateFlags bit (MS-OXOCAL 2.2.1.10) that
// marks an appointment received as an invitation rather than organized here.
const asfReceived int32 = 0x2

// errNoAttendees reports a scheduling message that has nobody to go to.
var errNoAttendees = errors.New("webmail2api: the meeting has no attendees")

// cleanAddresses reduces an attendee list to bare, lowercased SMTP addresses,
// deduplicated. An entry that does not parse is dropped rather than carried: it
// reaches an iCalendar ATTENDEE line and the To header of the scheduling mail, so an
// entry holding a line break would splice lines of the sender's choosing into both.
func cleanAddresses(list []string) []string {
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, a := range list {
		parsed, err := mail.ParseAddress(strings.TrimSpace(a))
		if err != nil {
			continue
		}
		addr := strings.ToLower(parsed.Address)
		if addr != "" && !seen[addr] {
			seen[addr] = true
			out = append(out, addr)
		}
	}
	return out
}

// hasAttendees reports whether the editor named anyone to meet with.
func hasAttendees(e eventJSON) bool {
	return len(cleanAddresses(e.Attendees))+len(cleanAddresses(e.OptionalAttendees)) > 0
}

// organizerOf is the organizer a stored appointment names: the representing
// identity the iCalendar import sets from ORGANIZER.
func organizerOf(props mapi.PropertyValues) string {
	return propStr(props, mapi.PrSentRepresentingSmtpAddress)
}

// isLegacyMeeting reports whether a stored meeting was organized here before its
// invitations carried the event's own UID: it has attendees but records no
// organizer, which this webmail now always writes for a meeting. Those invitations
// named the meeting by its message id, so that is the identity its attendees hold.
func isLegacyMeeting(st *objectstore.Store, id int64, props mapi.PropertyValues) bool {
	return organizerOf(props) == "" && len(attendeeAddresses(st, id)) > 0
}

// isOrganizer reports whether caller organizes the stored appointment: it was not
// received as an invitation, and the organizer it records, if any, is caller.
// Only the organizer sends a meeting its requests and cancellations; an attendee
// who edits or deletes their own copy tells nobody.
func isOrganizer(st *objectstore.Store, props mapi.PropertyValues, caller string) bool {
	if flags, ok := propInt32(props, namedLongTag(st, mapi.NameAppointmentStateFlags)); ok && flags&asfReceived != 0 {
		return false
	}
	org := organizerOf(props)
	return org == "" || strings.EqualFold(org, caller)
}

// storedSequence is the meeting revision the appointment records, 0 when it
// records none. A series keeps its revision only in the iCalendar stored verbatim,
// because the import synthesizes no properties for a recurring event beyond what a
// listing needs, so both places are read and the higher one wins.
func storedSequence(st *objectstore.Store, props mapi.PropertyValues) int {
	n, _ := propInt32(props, namedLongTag(st, mapi.NameAppointmentSequence))
	if v, ok := props.Get(mapi.PrIcalOriginal); ok {
		if raw, ok := v.([]byte); ok {
			return max(int(n), oxcical.Sequence(raw, nil))
		}
	}
	return int(n)
}

// namedLongTag resolves a PtLong named property the store already allocated, 0
// when it never did.
func namedLongTag(st *objectstore.Store, name mapi.PropertyName) mapi.PropTag {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{name})
	if err != nil || len(ids) != 1 || ids[0] == 0 {
		return 0
	}
	return mapi.MakeTag(ids[0], mapi.PtLong)
}

// meetingBody renders the stored appointment as the iCalendar its attendees hold,
// under the UID they know it by.
func meetingBody(st *objectstore.Store, id int64) ([]byte, mapi.PropertyValues, error) {
	msg, err := st.OpenMessage(id)
	if err != nil {
		return nil, nil, err
	}
	ical, err := oxcical.Export(msg, oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return nil, nil, err
	}
	if isLegacyMeeting(st, id, msg.Props) {
		if withUID, ok := oxcical.WithUID(ical, strconv.FormatInt(id, 10)); ok {
			ical = withUID
		}
	}
	return ical, msg.Props, nil
}

// meetingMail is one scheduling message about a stored meeting.
type meetingMail struct {
	method, subject, text, kind string
	calendar                    []byte
}

// meetingRecipients lists the attendees a scheduling message about the stored
// meeting goes to: every attendee that parses as an address, the organizer left out.
func meetingRecipients(st *objectstore.Store, id int64, organizer string) []string {
	var to []string
	for _, a := range cleanAddresses(attendeeAddresses(st, id)) {
		if !strings.EqualFold(a, organizer) {
			to = append(to, a)
		}
	}
	return to
}

// sendMeetingMail sends a scheduling message from the organizer to the meeting's
// attendees and files the Sent copy. The meeting is already stored when this runs,
// so the caller treats a failure as best-effort: it is recorded, and the stored
// change stands.
func (s *Server) sendMeetingMail(st *objectstore.Store, id int64, organizer string, m meetingMail) error {
	return s.deliverMeetingMail(st, organizer, meetingRecipients(st, id, organizer), m)
}

// deliverMeetingMail sends a scheduling message to the given attendees and files
// the Sent copy.
func (s *Server) deliverMeetingMail(st *objectstore.Store, organizer string, to []string, m meetingMail) error {
	if len(to) == 0 {
		return errNoAttendees
	}
	raw, err := itip.Message(itip.Mail{From: organizer, To: to, Subject: headerSafe(m.subject), Text: m.text, Calendar: m.calendar, Method: m.method})
	if err != nil {
		return err
	}
	if _, err := mta.DeliverAndRelay(s.accounts, s.spool, organizer, to, raw, time.Now()); err != nil {
		return err
	}
	fileSentCopy(st, raw, organizer, m.kind)
	return nil
}

// sendInvitation sends the stored meeting to its attendees as a METHOD:REQUEST:
// the whole object, series rule, time zones and exceptions included, under the UID
// the attendees know it by.
func (s *Server) sendInvitation(st *objectstore.Store, id int64, organizer, kind string, e eventJSON) {
	ical, props, err := meetingBody(st, id)
	if err == nil {
		if req, ok := oxcical.WithMethod(ical, "REQUEST"); ok {
			err = s.sendMeetingMail(st, id, organizer, meetingMail{
				method: "REQUEST", subject: propStr(props, mapi.PrSubject), text: inviteTextBody(e), kind: kind, calendar: req,
			})
		}
	}
	if err != nil && !errors.Is(err, errNoAttendees) {
		logError("send-"+kind, err, logging.Fields{"user": organizer})
	}
}

// pendingMail is a scheduling message prepared before the change it announces is
// stored, and sent only once it is.
type pendingMail struct {
	organizer string
	to        []string
	mail      meetingMail
}

// send delivers a prepared message. A failure is recorded and not returned: the
// change it announces is already stored.
func (p *pendingMail) send(s *Server, st *objectstore.Store) {
	if p == nil {
		return
	}
	if err := s.deliverMeetingMail(st, p.organizer, p.to, p.mail); err != nil && !errors.Is(err, errNoAttendees) {
		logError("send-"+p.mail.kind, err, logging.Fields{"user": p.organizer})
	}
}

// prepareCancellation builds the METHOD:CANCEL the organizer's deletion of a
// meeting sends its attendees, or nil when there is nothing to send: caller does
// not organize it, or it has nobody to tell. It is prepared before the deletion,
// because the deleted meeting can no longer be read, and sent after it, because a
// cancellation sent for a meeting that then failed to delete cannot be recalled.
func prepareCancellation(st *objectstore.Store, id int64, caller string) *pendingMail {
	ical, props, err := meetingBody(st, id)
	if err != nil || !isAppointment(props) || !isOrganizer(st, props, caller) {
		return nil
	}
	to := meetingRecipients(st, id, caller)
	body, ok := oxcical.CancelBody(ical, storedSequence(st, props)+1)
	if len(to) == 0 || !ok {
		return nil
	}
	subject := "Canceled: " + propStr(props, mapi.PrSubject)
	return &pendingMail{organizer: caller, to: to, mail: meetingMail{
		method: "CANCEL", subject: subject, text: subject, kind: "meeting-cancellation", calendar: body,
	}}
}

// inviteTextBody renders the plain-text body the invitee reads if their client
// does not render the calendar part.
func inviteTextBody(e eventJSON) string {
	when := "When: " + e.Start
	if e.End != "" {
		when += " - " + e.End
	}
	parts := []string{e.Summary, "", when}
	if e.Location != "" {
		parts = append(parts, "Where: "+e.Location)
	}
	if e.Description != "" {
		parts = append(parts, "", e.Description)
	}
	return strings.Join(parts, "\r\n")
}
