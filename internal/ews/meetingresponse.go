package ews

import (
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// PidLidResponseStatus values (MS-OXOCAL 2.2.1.11) — the attendee's standing on a
// meeting.
const (
	respTentative int32 = 2
	respAccepted  int32 = 3
	respDeclined  int32 = 4
)

// PidLidAppointmentStateFlags bits (MS-OXOCAL 2.2.1.10): the item is a meeting and
// was received as an invitation (rather than organized here).
const (
	asfMeeting  int32 = 0x1
	asfReceived int32 = 0x2
)

// PidLidBusyStatus values (MS-OXOCAL 2.2.1.2): the attendee's response sets how the
// appointment shows on their free/busy.
const (
	busyFree      int32 = 0
	busyTentative int32 = 1
	busyBusy      int32 = 2
)

// meetingResponse is an AcceptItem/TentativelyAcceptItem/DeclineItem response
// object ([MS-OXWSMTGS]): it references the meeting request it answers.
type meetingResponse struct {
	ReferenceItemID refID `xml:"ReferenceItemId"`
}

// meetingTags are the meeting-workflow named-property tags resolved against a
// mailbox once per response.
type meetingTags struct {
	resp, reply, state, busy, uid mapi.PropTag
}

func resolveMeetingTags(st *objectstore.Store) (meetingTags, error) {
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameResponseStatus,
		mapi.NameAppointmentReplyTime,
		mapi.NameAppointmentStateFlags,
		mapi.NameBusyStatus,
		mapi.NameICalUID,
	})
	if err != nil {
		return meetingTags{}, err
	}
	return meetingTags{
		resp:  namedTag(ids[0], mapi.PtLong),
		reply: namedTag(ids[1], mapi.PtSysTime),
		state: namedTag(ids[2], mapi.PtLong),
		busy:  namedTag(ids[3], mapi.PtLong),
		uid:   namedTag(ids[4], mapi.PtUnicode),
	}, nil
}

// meetingRespond records an attendee's response to the referenced meeting request:
// it stamps the request with the response, files the appointment in the Calendar
// for an accept or tentative (declining files none), and — when send is set
// (a SendOnly/SendAndSaveCopy disposition) — notifies the organizer with an iTIP
// REPLY. A decline still notifies; only the calendar leg is conditional.
func (s *Server) meetingRespond(st *objectstore.Store, sess *session, ref refID, response int32, send bool) itemResponseMessage {
	id, err := oxews.DecodeItemID(ref.ID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	req, err := st.OpenMessage(id.MessageID)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}
	tags, err := resolveMeetingTags(st)
	if err != nil {
		return itemError("ErrorInternalServerError")
	}
	now := mapi.UnixToNTTime(time.Now())

	// Stamp the request so it reads as responded.
	stamp := mapi.PropertyValues{
		{Tag: tags.resp, Value: response},
		{Tag: tags.reply, Value: now},
		{Tag: tags.state, Value: asfMeeting | asfReceived},
		{Tag: tags.busy, Value: meetingBusy(response)},
	}
	if err := st.ModifyMessageProperties(id.MessageID, stamp); err != nil {
		return itemError("ErrorInternalServerError")
	}

	// Accepting or tentatively accepting files the appointment; declining files none.
	if response != respDeclined {
		if err := fileAppointment(st, req, tags, response, now); err != nil {
			return itemError("ErrorInternalServerError")
		}
	}

	// Notify the organizer of the response (any of the three) when asked.
	if send {
		if err := s.notifyOrganizer(st, sess, req, response); err != nil {
			return itemError("ErrorInternalServerError")
		}
	}
	return meetingResponseOK()
}

// fileAppointment files (or, matched by iCalendar UID, updates) the Calendar
// appointment for an accepted/tentative meeting, from the request's own properties
// re-classed and carrying the response/state/busy stamps. (Envelope properties a
// real inbound meeting request would carry are not stripped yet; inbound calendar
// parsing is a later increment, and a request synthesized through oxcical carries
// none.)
func fileAppointment(st *objectstore.Store, req *oxcmail.Message, tags meetingTags, response int32, now uint64) error {
	cal := append(mapi.PropertyValues(nil), req.Props...)
	cal.Set(mapi.PrMessageClass, "IPM.Appointment")
	cal.Set(tags.resp, response)
	cal.Set(tags.reply, now)
	cal.Set(tags.state, asfMeeting|asfReceived)
	cal.Set(tags.busy, meetingBusy(response))

	uid := ""
	if v, ok := req.Props.Get(tags.uid); ok {
		uid, _ = v.(string)
	}
	if existing, ok := findCalendarByUID(st, tags.uid, uid); ok {
		return st.ModifyMessageProperties(existing, cal)
	}
	_, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: cal})
	return err
}

// notifyOrganizer sends the meeting organizer an iTIP REPLY for the response: the
// request is reshaped into a response message (re-classed, sent as the responder
// while keeping the organizer as the representing identity so oxcical's REPLY names
// them), rendered to an iCalendar REPLY carried as a text/calendar part, and routed
// like any submission. An organizer that did not request a response is not told.
func (s *Server) notifyOrganizer(st *objectstore.Store, sess *session, req *oxcmail.Message, response int32) error {
	organizer := propStr(req.Props, mapi.PrSentRepresentingSmtpAddress)
	if organizer == "" {
		organizer = propStr(req.Props, mapi.PrSenderSmtpAddress)
	}
	if organizer == "" || !responseRequested(req.Props) {
		return nil
	}

	resp := append(mapi.PropertyValues(nil), req.Props...)
	resp.Set(mapi.PrMessageClass, responseClass(response))
	resp.Set(mapi.PrSenderSmtpAddress, sess.user)
	resp.Set(mapi.PrSenderEmailAddress, sess.user)
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
	raw, err := oxcmail.Export(respMsg, oxcmail.Options{Resolver: st.GetNamedPropIDs, CalendarBody: ical, CalendarMethod: "REPLY"})
	if err != nil {
		return err
	}
	_, err = mta.DeliverAndRelay(s.accounts, s.Spool, sess.user, []string{organizer}, raw, time.Now())
	return err
}

// responseClass maps a response to the meeting-response message class oxcical reads
// to emit the REPLY's PARTSTAT.
func responseClass(response int32) string {
	switch response {
	case respAccepted:
		return "IPM.Schedule.Meeting.Resp.Pos"
	case respTentative:
		return "IPM.Schedule.Meeting.Resp.Tent"
	default:
		return "IPM.Schedule.Meeting.Resp.Neg"
	}
}

// responsePrefix is the human-readable subject prefix Exchange clients show for a
// meeting response.
func responsePrefix(response int32) string {
	switch response {
	case respAccepted:
		return "Accepted: "
	case respTentative:
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

// propStr reads a string-valued property, or "".
func propStr(props mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := props.Get(tag); ok {
		s, _ := v.(string)
		return s
	}
	return ""
}

// meetingBusy maps a response to the free/busy the resulting appointment shows.
func meetingBusy(response int32) int32 {
	switch response {
	case respAccepted:
		return busyBusy
	case respTentative:
		return busyTentative
	default:
		return busyFree
	}
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

// meetingResponseOK is a success response message for one meeting response. The
// empty Items container is present because clients reject a CreateItemResponseMessage
// without one.
func meetingResponseOK() itemResponseMessage {
	return itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", Items: &itemsWrap{}}
}
