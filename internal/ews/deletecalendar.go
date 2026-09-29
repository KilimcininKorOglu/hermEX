package ews

import (
	"errors"
	"strings"
	"time"

	"hermex/internal/itip"
	"hermex/internal/mapi"
	"hermex/internal/meeting"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// asfReceived is the PidLidAppointmentStateFlags bit (MS-OXOCAL 2.2.1.10) of a
// meeting the mailbox received as an invitation rather than organized.
const asfReceived int32 = 0x2

// schedulingMail is one iTIP message about a stored meeting, prepared before the
// change it announces is stored and sent only once it is.
type schedulingMail struct {
	organizer string
	to        []string
	subject   string
	method    string
	calendar  []byte
}

// sendScheduling sends a scheduling message as the request's disposition
// (SendMeetingInvitations or SendMeetingCancellations) asks and files the Sent
// copy SendToAllAndSaveCopy asks for. The change it announces is stored by then,
// so a failure is recorded rather than reported. It reports whether the message
// went out.
func (s *Server) sendScheduling(st *objectstore.Store, m *schedulingMail, disposition string) bool {
	if m == nil || len(m.to) == 0 || disposition == "SendToNone" {
		return false
	}
	subject := strings.NewReplacer("\r", " ", "\n", " ").Replace(m.subject)
	raw, err := itip.Message(itip.Mail{From: m.organizer, To: m.to, Subject: subject, Text: subject, Calendar: m.calendar, Method: m.method})
	if err == nil {
		_, err = mta.DeliverAndRelay(s.accounts, s.Spool, m.organizer, m.to, raw, time.Now())
	}
	if err != nil {
		st.LogSwallowedError("ews.meeting_"+strings.ToLower(m.method), err)
		return false
	}
	if disposition == "SendToAllAndSaveCopy" {
		if _, err := st.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, time.Now(), int64(objectstore.FlagSeen)); err != nil {
			st.LogSwallowedError("ews.meeting_sent_copy", err)
		}
	}
	return true
}

// objectDelete is one DeleteItem of an item the object store alone holds: a
// calendar item or one occurrence of a series, a task or a note.
type objectDelete struct {
	st            *objectstore.Store
	id            oxews.ItemID
	deleteType    string
	cancellations string // SendMeetingCancellations
	caller        string
}

// deleteObjectItem deletes an object-store item and, when the caller organizes a
// meeting that went out, sends its attendees the cancellation. It returns the
// response code of a refusal, "" on success.
func (s *Server) deleteObjectItem(d objectDelete) string {
	msg, err := d.st.OpenMessage(d.id.MessageID)
	if err != nil {
		return "ErrorItemNotFound"
	}
	appointment := isAppointment(itemClass(msg.Props))
	if appointment && d.cancellations == "" {
		return "ErrorSendMeetingCancellationsRequired"
	}
	if d.id.Instance != 0 {
		return s.deleteOccurrence(d, msg)
	}
	var notice *schedulingMail
	if appointment {
		notice = cancellation(d.st, msg, d.caller)
	}
	if err := removeObject(d.st, d.id.MessageID, d.deleteType); err != nil {
		return "ErrorItemNotFound"
	}
	s.sendScheduling(d.st, notice, d.cancellations)
	return ""
}

// removeObject deletes an object-store item as the DeleteType asks: into Deleted
// Items for MoveToDeletedItems, else into the Recoverable Items dumpster. The item
// keeps its id when it moves.
func removeObject(st *objectstore.Store, id int64, deleteType string) error {
	if deleteType == "HardDelete" || deleteType == "SoftDelete" {
		return st.SoftDeleteObject(id)
	}
	fid, err := st.MessageFolder(id)
	if err != nil {
		return err
	}
	if fid == int64(mapi.PrivateFIDDeletedItems) {
		return st.SoftDeleteObject(id)
	}
	_, err = st.MoveMessageImport(fid, id, int64(mapi.PrivateFIDDeletedItems), id)
	return err
}

// deleteOccurrence removes one occurrence from its series, leaving the others,
// and tells the attendees when the caller organizes the meeting. A series a MAPI
// client wrote keeps no iCalendar to remove the one instance from, and is refused
// rather than deleted whole.
func (s *Server) deleteOccurrence(d objectDelete, msg *oxcmail.Message) string {
	before, ok := verbatimICal(msg.Props)
	if !ok {
		return "ErrorItemSave"
	}
	at := time.Unix(d.id.Instance, 0).UTC()
	if !oxcical.HasInstance(before, at) {
		return "ErrorCalendarOccurrenceIsDeletedFromRecurrence"
	}
	edited, ok := oxcical.CancelOccurrence(before, at)
	if !ok {
		return "ErrorItemNotFound"
	}
	edited, notice := occurrenceCancellation(d.st, msg, d.caller, before, edited, at)
	if err := meeting.ReplaceSeries(d.st, d.id.MessageID, edited); err != nil {
		return "ErrorItemSave"
	}
	s.sendScheduling(d.st, notice, d.cancellations)
	return ""
}

// occurrenceCancellation advances the series revision in the edited series and
// prepares the single-instance cancellation, describing the instance as it stood
// before the edit removed it. It returns the edited series unchanged and no message
// when there is nobody to tell.
func occurrenceCancellation(st *objectstore.Store, msg *oxcmail.Message, caller string, before, edited []byte, at time.Time) ([]byte, *schedulingMail) {
	to := meetingAudience(st, msg, caller)
	if len(to) == 0 {
		return edited, nil
	}
	seq := max(oxcical.Sequence(before, nil), oxcical.Sequence(before, &at)) + 1
	revised, ok := oxcical.SetSequence(edited, seq, nil)
	if !ok {
		return edited, nil
	}
	body, ok := oxcical.InstanceBody(before, at, "CANCEL", seq)
	if !ok {
		return edited, nil
	}
	return revised, &schedulingMail{organizer: caller, to: to, subject: "Canceled: " + strProp(msg.Props, mapi.PrSubject), method: "CANCEL", calendar: body}
}

// cancellation prepares the METHOD:CANCEL the organizer's deletion of a meeting
// sends its attendees, nil when there is nobody to tell. It is prepared before the
// deletion, because the deleted meeting can no longer be read.
func cancellation(st *objectstore.Store, msg *oxcmail.Message, caller string) *schedulingMail {
	to := meetingAudience(st, msg, caller)
	if len(to) == 0 {
		return nil
	}
	ical, err := oxcical.Export(msg, oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		st.LogSwallowedError("ews.meeting_cancel", err)
		return nil
	}
	body, ok := oxcical.CancelBody(ical, oxcical.Sequence(ical, nil)+1)
	if !ok {
		st.LogSwallowedError("ews.meeting_cancel", errors.New("ews: the meeting does not render as a cancellation"))
		return nil
	}
	return &schedulingMail{organizer: caller, to: to, subject: "Canceled: " + strProp(msg.Props, mapi.PrSubject), method: "CANCEL", calendar: body}
}

// meetingAudience lists the attendees a change to the stored meeting is announced
// to: none unless caller organizes it and its request went out, else every
// attendee address that parses, the organizer left out.
func meetingAudience(st *objectstore.Store, msg *oxcmail.Message, caller string) []string {
	if !organizes(st, msg.Props, caller) || neverInvited(st, msg.Props) {
		return nil
	}
	var to []string
	seen := map[string]bool{caller: true}
	for _, row := range msg.Recipients {
		if longProp(row, mapi.PrRecipientFlags)&recipOrganizer != 0 {
			continue
		}
		addr := strProp(row, mapi.PrSmtpAddress)
		if addr == "" {
			addr = strProp(row, mapi.PrEmailAddress)
		}
		if clean, ok := cleanAddress(addr); ok && !seen[clean] {
			seen[clean] = true
			to = append(to, clean)
		}
	}
	return to
}

// organizes reports whether caller organizes the stored meeting: it was not
// received as an invitation, and the organizer it records, if any, is caller.
func organizes(st *objectstore.Store, props mapi.PropertyValues, caller string) bool {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameAppointmentStateFlags})
	if err != nil {
		st.LogSwallowedError("ews.meeting_state", err)
		return false
	}
	if longProp(props, mapi.MakeTag(ids[0], mapi.PtLong))&asfReceived != 0 {
		return false
	}
	org := organizerMailbox(props)
	return org == nil || strings.EqualFold(org.EmailAddress, caller)
}

// neverInvited reports whether the stored meeting records that its request was
// never sent (PidLidFInvited false). One that records nothing either way counts as
// sent, so its attendees still hear about a cancellation.
func neverInvited(st *objectstore.Store, props mapi.PropertyValues) bool {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameFInvited})
	if err != nil || ids[0] == 0 {
		return false
	}
	v, ok := props.Get(mapi.MakeTag(ids[0], mapi.PtBoolean))
	sent, isBool := v.(bool)
	return ok && isBool && !sent
}

// verbatimICal reads the iCalendar a stored calendar item keeps verbatim.
func verbatimICal(props mapi.PropertyValues) ([]byte, bool) {
	v, ok := props.Get(mapi.PrIcalOriginal)
	if !ok {
		return nil, false
	}
	raw, ok := v.([]byte)
	return raw, ok && len(raw) > 0
}
