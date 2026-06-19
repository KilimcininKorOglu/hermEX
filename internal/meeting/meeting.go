// Package meeting carries the protocol-neutral meeting-response workflow shared by
// EWS (MS-OXWSMTGS) and ActiveSync (MS-ASCMD MeetingResponse): recording an
// attendee's accept/tentative/decline on a meeting request — stamping the request,
// filing the appointment in the Calendar, and notifying the organizer with an iTIP
// REPLY. The orchestration lives here so each protocol handler only decodes its own
// request and renders its own response.
package meeting

import (
	"errors"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/relay"
)

// Response is an attendee's standing on a meeting (PidLidResponseStatus, MS-OXOCAL
// 2.2.1.11).
const (
	ResponseTentative int32 = 2
	ResponseAccepted  int32 = 3
	ResponseDeclined  int32 = 4
)

// PidLidAppointmentStateFlags bits (MS-OXOCAL 2.2.1.10): the item is a meeting and
// was received as an invitation rather than organized here.
const (
	asfMeeting  int32 = 0x1
	asfReceived int32 = 0x2
)

// PidLidBusyStatus values (MS-OXOCAL 2.2.1.2): the response sets how the
// appointment shows on the attendee's free/busy.
const (
	busyFree      int32 = 0
	busyTentative int32 = 1
	busyBusy      int32 = 2
)

// ErrRequestNotFound is returned when the referenced meeting request cannot be
// opened, so a protocol handler can map it to its own not-found status.
var ErrRequestNotFound = errors.New("meeting: request not found")

// Tags are the meeting-workflow named-property tags resolved against a mailbox once
// per response.
type Tags struct {
	Resp, Reply, State, Busy, UID mapi.PropTag
}

// ResolveTags resolves (allocating when absent) the meeting-workflow named tags.
func ResolveTags(st *objectstore.Store) (Tags, error) {
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameResponseStatus,
		mapi.NameAppointmentReplyTime,
		mapi.NameAppointmentStateFlags,
		mapi.NameBusyStatus,
		mapi.NameICalUID,
	})
	if err != nil {
		return Tags{}, err
	}
	return Tags{
		Resp:  mapi.MakeTag(ids[0], mapi.PtLong),
		Reply: mapi.MakeTag(ids[1], mapi.PtSysTime),
		State: mapi.MakeTag(ids[2], mapi.PtLong),
		Busy:  mapi.MakeTag(ids[3], mapi.PtLong),
		UID:   mapi.MakeTag(ids[4], mapi.PtUnicode),
	}, nil
}

// Respond records an attendee's response to the meeting request at messageID: it
// stamps the request as responded, files the appointment in the Calendar for an
// accept or tentative (declining files none), and — when send is set — notifies the
// organizer with an iTIP REPLY routed from sender. It returns the message id of the
// filed Calendar appointment (0 when declined). sender is the responder's address;
// accounts and spool route the organizer notification.
func Respond(st *objectstore.Store, accounts directory.Accounts, spool *relay.Spool, sender string, messageID int64, response int32, send bool) (int64, error) {
	req, err := st.OpenMessage(messageID)
	if err != nil {
		return 0, ErrRequestNotFound
	}
	tags, err := ResolveTags(st)
	if err != nil {
		return 0, err
	}
	now := mapi.UnixToNTTime(time.Now())

	if err := st.ModifyMessageProperties(messageID, mapi.PropertyValues{
		{Tag: tags.Resp, Value: response},
		{Tag: tags.Reply, Value: now},
		{Tag: tags.State, Value: asfMeeting | asfReceived},
		{Tag: tags.Busy, Value: meetingBusy(response)},
	}); err != nil {
		return 0, err
	}

	var calendarID int64
	if response == ResponseDeclined {
		// Declining takes the meeting off the calendar: remove any appointment a
		// prior accept or tentative filed (the reference's doDecline deletes the
		// calendar items matching the meeting's UID).
		if err := removeAppointment(st, req, tags); err != nil {
			return 0, err
		}
	} else if calendarID, err = file(st, req, tags, response, now); err != nil {
		return 0, err
	}
	if send {
		if err := notifyOrganizer(st, accounts, spool, sender, req, response); err != nil {
			return 0, err
		}
	}
	return calendarID, nil
}

// file files (or, matched by iCalendar UID, updates) the Calendar appointment for an
// accepted/tentative meeting from the request's own properties, re-classed and
// carrying the response stamps. It returns the appointment's message id.
func file(st *objectstore.Store, req *oxcmail.Message, tags Tags, response int32, now uint64) (int64, error) {
	cal := append(mapi.PropertyValues(nil), req.Props...)
	cal.Set(mapi.PrMessageClass, "IPM.Appointment")
	cal.Set(tags.Resp, response)
	cal.Set(tags.Reply, now)
	cal.Set(tags.State, asfMeeting|asfReceived)
	cal.Set(tags.Busy, meetingBusy(response))

	if existing, ok := findCalendarByUID(st, tags.UID, uidOf(req.Props, tags)); ok {
		return existing, st.ModifyMessageProperties(existing, cal)
	}
	return st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: cal})
}

// removeAppointment deletes the Calendar appointment a prior accept or tentative
// filed for the meeting request, matched by its iCalendar UID. A request with no
// filed appointment (or no UID) removes nothing.
func removeAppointment(st *objectstore.Store, req *oxcmail.Message, tags Tags) error {
	if existing, ok := findCalendarByUID(st, tags.UID, uidOf(req.Props, tags)); ok {
		return st.DeleteObject(existing)
	}
	return nil
}

// uidOf reads the iCalendar UID a scheduling message carries, or "".
func uidOf(props mapi.PropertyValues, tags Tags) string {
	if v, ok := props.Get(tags.UID); ok {
		s, _ := v.(string)
		return s
	}
	return ""
}

// notifyOrganizer sends the organizer an iTIP REPLY for the response: the request is
// reshaped into a response message (re-classed, sent as the responder while keeping
// the organizer as the representing identity so oxcical's REPLY names them), rendered
// to an iCalendar REPLY carried as a text/calendar part, and routed like any
// submission. An organizer that did not request a response is not told.
func notifyOrganizer(st *objectstore.Store, accounts directory.Accounts, spool *relay.Spool, sender string, req *oxcmail.Message, response int32) error {
	organizer := propStr(req.Props, mapi.PrSentRepresentingSmtpAddress)
	if organizer == "" {
		organizer = propStr(req.Props, mapi.PrSenderSmtpAddress)
	}
	if organizer == "" || !responseRequested(req.Props) {
		return nil
	}

	resp := append(mapi.PropertyValues(nil), req.Props...)
	resp.Set(mapi.PrMessageClass, responseClass(response))
	resp.Set(mapi.PrSenderSmtpAddress, sender)
	resp.Set(mapi.PrSenderEmailAddress, sender)
	resp.Set(mapi.PrSenderAddrType, "SMTP")
	resp.Set(mapi.PrSubject, responsePrefix(response)+propStr(req.Props, mapi.PrSubject))
	resp.Set(mapi.PrClientSubmitTime, mapi.UnixToNTTime(time.Now()))

	respMsg := &oxcmail.Message{
		Props: resp,
		Recipients: []mapi.PropertyValues{{
			{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
			{Tag: mapi.PrSmtpAddress, Value: organizer},
			{Tag: mapi.PrDisplayName, Value: propStr(req.Props, mapi.PrSentRepresentingName)},
		}},
	}

	ical, err := oxcical.Export(respMsg, oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return err
	}
	oxcmail.EnsureMessageID(&respMsg.Props)
	raw, err := oxcmail.Export(respMsg, oxcmail.Options{Resolver: st.GetNamedPropIDs, CalendarBody: ical, CalendarMethod: "REPLY"})
	if err != nil {
		return err
	}
	_, err = mta.DeliverAndRelay(accounts, spool, sender, []string{organizer}, raw, time.Now())
	return err
}

// meetingBusy maps a response to the free/busy the resulting appointment shows.
func meetingBusy(response int32) int32 {
	switch response {
	case ResponseAccepted:
		return busyBusy
	case ResponseTentative:
		return busyTentative
	default:
		return busyFree
	}
}

// responseClass maps a response to the meeting-response message class oxcical reads
// to emit the REPLY's PARTSTAT.
func responseClass(response int32) string {
	switch response {
	case ResponseAccepted:
		return "IPM.Schedule.Meeting.Resp.Pos"
	case ResponseTentative:
		return "IPM.Schedule.Meeting.Resp.Tent"
	default:
		return "IPM.Schedule.Meeting.Resp.Neg"
	}
}

// responsePrefix is the human-readable subject prefix Exchange clients show for a
// meeting response.
func responsePrefix(response int32) string {
	switch response {
	case ResponseAccepted:
		return "Accepted: "
	case ResponseTentative:
		return "Tentative: "
	default:
		return "Declined: "
	}
}

// responseRequested reports whether the organizer wants a response — true unless
// PR_RESPONSE_REQUESTED is explicitly false.
func responseRequested(props mapi.PropertyValues) bool {
	if v, ok := props.Get(mapi.PrResponseRequested); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return true
}

// findCalendarByUID returns the message id of a Calendar item carrying the given
// iCalendar UID, so re-responding to a meeting updates its appointment rather than
// filing a duplicate. An empty UID matches nothing.
func findCalendarByUID(st *objectstore.Store, uidTag mapi.PropTag, uid string) (int64, bool) {
	if uid == "" {
		return 0, false
	}
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil {
		return 0, false
	}
	for _, obj := range objs {
		pv, err := st.GetMessageProperties(obj.ID, uidTag)
		if err != nil {
			continue
		}
		if v, ok := pv.Get(uidTag); ok {
			if s, _ := v.(string); s == uid {
				return obj.ID, true
			}
		}
	}
	return 0, false
}

// propStr reads a string-valued property, or "".
func propStr(props mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := props.Get(tag); ok {
		s, _ := v.(string)
		return s
	}
	return ""
}
