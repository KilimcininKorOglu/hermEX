// Package meeting carries the protocol-neutral meeting-response workflow shared by
// EWS (MS-OXWSMTGS) and ActiveSync (MS-ASCMD MeetingResponse): recording an
// attendee's accept/tentative/decline on a meeting request, stamping the request,
// filing the appointment in the Calendar, and notifying the organizer with an iTIP
// REPLY. The orchestration lives here so each protocol handler only decodes its own
// request and renders its own response.
package meeting

import (
	"errors"
	"slices"
	"strings"
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
	busyFree      = mapi.BusyFree
	busyTentative = mapi.BusyTentative
	busyBusy      = mapi.BusyBusy
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

// Respond records an ATTENDEE'S OWN response to the meeting request at messageID:
// it stamps the request as responded, files the appointment in the Calendar for an
// accept or tentative (declining files none), and, when send is set, notifies the
// organizer with an iTIP REPLY routed from sender. It returns the message id of the
// filed Calendar appointment (0 when declined). sender is the responder's address;
// accounts and spool route the organizer notification.
//
// It is the path a person took, which is what lets it clear the request mail when
// the mailbox asked for that. A response the SERVER decided goes through
// respondAutomatically instead.
func Respond(st *objectstore.Store, accounts directory.Accounts, spool *relay.Spool, sender string, messageID int64, response int32, send bool) (int64, error) {
	return respond(st, accounts, spool, sender, messageID, response, send, true)
}

// respondAutomatically records a response the server decided on the mailbox's
// behalf. It never clears the request mail: the reader has not seen the invitation
// yet, and a message that disappears without anyone acting on it is one they can
// no longer find.
func respondAutomatically(st *objectstore.Store, accounts directory.Accounts, spool *relay.Spool, sender string, messageID int64, response int32, send bool) (int64, error) {
	return respond(st, accounts, spool, sender, messageID, response, send, false)
}

func respond(st *objectstore.Store, accounts directory.Accounts, spool *relay.Spool, sender string, messageID int64, response int32, send bool, userAction bool) (int64, error) {
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
	// The cleanup is a courtesy and runs after the response is already recorded,
	// the appointment booked or removed, and the organizer told. Failing the call
	// here would report failure for work that completed, and a client retry would
	// send the organizer a second reply, so the failure is logged and swallowed.
	clearAnsweredRequest(st, req, tags, messageID, userAction)
	return calendarID, nil
}

// clearAnsweredRequest takes the answered request out of the Inbox, but only for
// a response the reader gave. A response the server decided leaves the mail where
// the reader can still find it.
func clearAnsweredRequest(st *objectstore.Store, req *oxcmail.Message, tags Tags, messageID int64, userAction bool) {
	if !userAction {
		return
	}
	if err := removeRequestMail(st, req, tags, messageID); err != nil {
		st.LogSwallowedError("meeting.remove-request", err)
	}
}

// requestClass is the message class a delivered meeting invitation carries.
const requestClass = "IPM.Schedule.Meeting.Request"

// removeHook fails the request cleanup on demand, so a test can prove a failure
// there does not fail the response. Production never assigns it; it mirrors the
// seams the object store keeps on its backup and regeneration paths.
var removeHook func() error

// removeRequestMail takes the answered request out of the Inbox when the mailbox
// asks for it, the way Outlook's "delete meeting requests and notifications from
// Inbox after responding" option does. It moves the mail to Deleted Items rather
// than deleting it, so the response stays recoverable from the folder a user looks
// in first. A mailbox that has not turned the setting on keeps the request.
//
// The answered object is NOT always the request. Responding from the calendar
// answers the appointment, so the request the user is done with has to be found in
// the Inbox by the meeting's UID; moving the answered object itself would file the
// appointment away. Responding from the mail view answers the request directly and
// moves that message.
func removeRequestMail(st *objectstore.Store, answered *oxcmail.Message, tags Tags, messageID int64) error {
	if removeHook != nil {
		if err := removeHook(); err != nil {
			return err
		}
	}
	cfg, err := st.GetMeetingConfig()
	if err != nil || !cfg.RemoveRequestOnResponse {
		return err
	}
	if propStr(answered.Props, mapi.PrMessageClass) == requestClass {
		return fileAway(st, messageID)
	}
	// Every invitation for this meeting, not just the first: an organizer who
	// updates a meeting sends another one, so the Inbox can hold several, and
	// leaving the rest is the state this setting exists to avoid.
	var errs []error
	for _, id := range findRequestsByUID(st, tags.UID, uidOf(answered.Props, tags)) {
		if err := fileAway(st, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// fileAway moves one message to Deleted Items. A message with no IMAP index entry
// has no (folder, UID) to move and is left alone, and so is one already in Deleted
// Items, because moving a message onto itself would allocate a new UID for nothing.
func fileAway(st *objectstore.Store, messageID int64) error {
	folderID, uid, ok, err := st.MessageIndexLocation(messageID)
	if err != nil || !ok || folderID == int64(mapi.PrivateFIDDeletedItems) {
		return err
	}
	_, err = st.MoveMessage(folderID, uid, int64(mapi.PrivateFIDDeletedItems))
	return err
}

// findRequestsByUID returns every Inbox meeting request carrying the given
// iCalendar UID, the counterpart of findCalendarByUID. It matches the request class
// only, so a response or a cancellation sharing the UID is left where it is. An
// empty UID matches nothing.
func findRequestsByUID(st *objectstore.Store, uidTag mapi.PropTag, uid string) []int64 {
	if uid == "" {
		return nil
	}
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDInbox))
	if err != nil {
		return nil
	}
	var out []int64
	for _, obj := range objs {
		pv, err := st.GetMessageProperties(obj.ID, uidTag, mapi.PrMessageClass)
		if err != nil {
			continue
		}
		if propStr(pv, mapi.PrMessageClass) != requestClass {
			continue
		}
		if v, ok := pv.Get(uidTag); ok {
			if s, _ := v.(string); s == uid {
				out = append(out, obj.ID)
			}
		}
	}
	return out
}

// file files (or, matched by iCalendar UID, updates) the Calendar appointment for an
// accepted/tentative meeting from the request's own properties, re-classed and
// carrying the response stamps. It returns the appointment's message id.
func file(st *objectstore.Store, req *oxcmail.Message, tags Tags, response int32, now uint64) (int64, error) {
	cal := stripInboundCruft(req.Props)
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

// inboundCruft is the set of inbound-mail properties that must not ride along when a
// delivered request is reshaped into a derived object: a filed appointment and an
// organizer response are new messages, not the email that carried the invitation.
// In particular the request's Message-ID must be dropped so the response mints its
// own (two messages may not share one), and the verbatim transport headers and
// threading have no place on either.
var inboundCruft = []mapi.PropTag{
	mapi.PrInternetMessageID,
	mapi.PrInternetReferences,
	mapi.PrInReplyToID,
	mapi.PrTransportMessageHeaders,
}

// stripInboundCruft copies props with the inbound-mail cruft removed.
func stripInboundCruft(props mapi.PropertyValues) mapi.PropertyValues {
	out := make(mapi.PropertyValues, 0, len(props))
	for _, pv := range props {
		if !slices.Contains(inboundCruft, pv.Tag) {
			out = append(out, pv)
		}
	}
	return out
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

	resp := stripInboundCruft(req.Props)
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

// responseRequested reports whether the organizer wants a response, true unless
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
	// The store resolves the match. Listing the calendar and reading the property
	// back one object at a time costs a query per appointment to find one of them,
	// and this runs on the delivery path for every inbound meeting message.
	id, found, err := st.FindObjectByProperty(int64(mapi.PrivateFIDCalendar), uidTag, uid)
	if err != nil {
		st.LogSwallowedError("meeting.find-by-uid", err)
		return 0, false
	}
	return id, found
}

// ApplyReply processes an incoming iTIP REPLY on the organizer's side: it locates
// the organizer's calendar event by the REPLY's iCalendar UID, finds the attendee
// (recipient) by SMTP address, and updates that recipient's PidLidResponseStatus
// to the response the attendee sent (accepted/tentative/declined). This is the
// data the organizer's TrackingTab reads. It is a no-op (returns nil) when the
// event or the attendee is not found, so a stray REPLY never fails delivery.
func ApplyReply(st *objectstore.Store, tags Tags, uid, attendeeEmail string, response int32) error {
	eventID, ok := findCalendarByUID(st, tags.UID, uid)
	if !ok {
		return nil
	}
	recipients, err := st.ListRecipients(eventID)
	if err != nil {
		return err
	}
	target := strings.ToLower(strings.TrimSpace(attendeeEmail))
	for _, r := range recipients {
		if strings.ToLower(strings.TrimSpace(r.SmtpAddress)) == target && target != "" {
			var props mapi.PropertyValues
			props.Set(tags.Resp, response)
			return st.SetRecipientProperties(r.ID, props)
		}
	}
	return nil
}

// propStr reads a string-valued property, or "".
func propStr(props mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := props.Get(tag); ok {
		s, _ := v.(string)
		return s
	}
	return ""
}
