package ews

import (
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
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

// meetingRespond records an attendee's response to the referenced meeting request:
// it stamps the request itself with the response, and — for accept or tentative —
// files (or, matched by iCalendar UID, updates) the appointment in the Calendar
// with the matching free/busy. Declining files no appointment.
//
// This is the attendee-visible leg only. A SendOnly/SendAndSaveCopy disposition
// also asks for the organizer to be told; that machine-readable REPLY needs
// calendar-MIME support and is a later increment, so it is not sent here.
func meetingRespond(st *objectstore.Store, ref refID, response int32) itemResponseMessage {
	id, err := oxews.DecodeItemID(ref.ID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	req, err := st.OpenMessage(id.MessageID)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}

	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameResponseStatus,
		mapi.NameAppointmentReplyTime,
		mapi.NameAppointmentStateFlags,
		mapi.NameBusyStatus,
		mapi.NameICalUID,
	})
	if err != nil {
		return itemError("ErrorInternalServerError")
	}
	respTag := namedTag(ids[0], mapi.PtLong)
	replyTag := namedTag(ids[1], mapi.PtSysTime)
	stateTag := namedTag(ids[2], mapi.PtLong)
	busyTag := namedTag(ids[3], mapi.PtLong)
	uidTag := namedTag(ids[4], mapi.PtUnicode)

	now := mapi.UnixToNTTime(time.Now())
	busy := meetingBusy(response)

	// Stamp the request so it reads as responded.
	stamp := mapi.PropertyValues{
		{Tag: respTag, Value: response},
		{Tag: replyTag, Value: now},
		{Tag: stateTag, Value: asfMeeting | asfReceived},
		{Tag: busyTag, Value: busy},
	}
	if err := st.ModifyMessageProperties(id.MessageID, stamp); err != nil {
		return itemError("ErrorInternalServerError")
	}

	// Declining files no calendar item — the response lives on the request only.
	if response == respDeclined {
		return meetingResponseOK()
	}

	// File the appointment from the request's own properties, re-classed and
	// carrying the response/state/busy stamps. (Envelope properties a real inbound
	// meeting request would carry are not stripped yet; inbound calendar parsing is
	// itself a later increment, and a request synthesized through oxcical carries
	// none.)
	cal := append(mapi.PropertyValues(nil), req.Props...)
	cal.Set(mapi.PrMessageClass, "IPM.Appointment")
	cal.Set(respTag, response)
	cal.Set(replyTag, now)
	cal.Set(stateTag, asfMeeting|asfReceived)
	cal.Set(busyTag, busy)

	uid := ""
	if v, ok := req.Props.Get(uidTag); ok {
		uid, _ = v.(string)
	}
	if existing, ok := findCalendarByUID(st, uidTag, uid); ok {
		if err := st.ModifyMessageProperties(existing, cal); err != nil {
			return itemError("ErrorInternalServerError")
		}
		return meetingResponseOK()
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: cal}); err != nil {
		return itemError("ErrorInternalServerError")
	}
	return meetingResponseOK()
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
