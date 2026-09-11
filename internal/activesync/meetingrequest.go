package activesync

import (
	"encoding/base64"
	"encoding/binary"
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// meetingMessageType values ([MS-ASEMAIL] 2.2.2.47): what kind of meeting message
// this is. Only the two a delivered invitation can be are emitted, an initial
// request and a full update; the rest describe states this server does not record.
const (
	meetingMsgInitial    = "1"
	meetingMsgFullUpdate = "2"
)

// meetingInstanceSingle is the MS-ASEMAIL InstanceType for a single appointment,
// the only kind this block describes today.
const meetingInstanceSingle = "0"

// meetingRequestTags holds the appointment named-property tags a delivered
// invitation is rendered from.
type meetingRequestTags struct {
	start, end, allDay, busy, loc, uid, sequence mapi.PropTag
}

// resolveMeetingRequestTags resolves those tags without allocating: a mailbox that
// never stored an appointment has no invitation to render, and an unresolved id
// simply reads as an absent property.
func resolveMeetingRequestTags(st *objectstore.Store) (meetingRequestTags, error) {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole, // 0
		mapi.NameAppointmentEndWhole,   // 1
		mapi.NameAppointmentSubType,    // 2
		mapi.NameBusyStatus,            // 3
		mapi.NameAppointmentLocation,   // 4
		mapi.NameICalUID,               // 5
		mapi.NameAppointmentSequence,   // 6
	})
	if err != nil {
		return meetingRequestTags{}, err
	}
	return meetingRequestTags{
		start:    mapi.MakeTag(ids[0], mapi.PtSysTime),
		end:      mapi.MakeTag(ids[1], mapi.PtSysTime),
		allDay:   mapi.MakeTag(ids[2], mapi.PtBoolean),
		busy:     mapi.MakeTag(ids[3], mapi.PtLong),
		loc:      mapi.MakeTag(ids[4], mapi.PtUnicode),
		uid:      mapi.MakeTag(ids[5], mapi.PtUnicode),
		sequence: mapi.MakeTag(ids[6], mapi.PtLong),
	}, nil
}

// meetingRequestNode builds the MS-ASEMAIL MeetingRequest container a delivered
// invitation carries. A device shows Accept / Tentative / Decline for a message that
// has one, and answers with the MeetingResponse command; a message without one is an
// ordinary mail to it. It returns nil when the message carries no appointment times,
// because DtStamp and the span are what the block exists to deliver.
//
// protocol decides the identifier: 14.x carries email:GlobalObjId, 16.x carries
// calendar:UID instead, and email2:MeetingMessageType is required from 14.1 onward.
func meetingRequestNode(st *objectstore.Store, messageID int64, protocol string) *wbxml.Node {
	tags, err := resolveMeetingRequestTags(st)
	if err != nil {
		st.LogSwallowedError("activesync.meeting-tags", err)
		return nil
	}
	pv, err := st.GetMessageProperties(messageID, tags.start, tags.end, tags.allDay, tags.busy,
		tags.loc, tags.uid, tags.sequence, mapi.PrLastModificationTime, mapi.PrResponseRequested,
		mapi.PrSentRepresentingSmtpAddress, mapi.PrSenderSmtpAddress)
	if err != nil {
		st.LogSwallowedError("activesync.meeting-props", err)
		return nil
	}
	start, ok := ntTimeProp(pv, tags.start)
	if !ok {
		return nil
	}
	end, ok := ntTimeProp(pv, tags.end)
	if !ok {
		return nil
	}
	stamp := start
	if mod, ok := ntTimeProp(pv, mapi.PrLastModificationTime); ok {
		stamp = mod
	}

	node := wbxml.Elem(wbxml.EMMeetingRequest,
		wbxml.Str(wbxml.EMAllDayEvent, boolStr(boolProp(pv, tags.allDay))),
		wbxml.Str(wbxml.EMStartTime, easCalTime(start)),
		wbxml.Str(wbxml.EMDtStamp, easCalTime(stamp)),
		wbxml.Str(wbxml.EMEndTime, easCalTime(end)),
		wbxml.Str(wbxml.EMInstanceType, meetingInstanceSingle),
	)
	if loc := stringProp(pv, tags.loc); loc != "" {
		node.Children = append(node.Children, wbxml.Str(wbxml.EMLocation, loc))
	}
	if org := meetingOrganizer(pv); org != "" {
		node.Children = append(node.Children, wbxml.Str(wbxml.EMOrganizer, org))
	}
	node.Children = append(node.Children,
		wbxml.Str(wbxml.EMResponseRequested, boolStr(meetingResponseRequested(pv))),
		wbxml.Str(wbxml.EMBusyStatus, strconv.Itoa(int(longProp(pv, tags.busy)))),
		wbxml.Str(wbxml.EMTimeZone, utcTimezone))
	node.Children = append(node.Children, meetingIdentifier(protocol, stringProp(pv, tags.uid)))
	if protocol != "14.0" {
		node.Children = append(node.Children,
			wbxml.Str(wbxml.EM2MeetingMessageType, meetingMessageType(longProp(pv, tags.sequence))))
	}
	return node
}

// meetingIdentifier names the meeting so the device can match it against a calendar
// object it already holds: GlobalObjId through 14.1, and calendar:UID from 16.0, the
// element that replaced it.
func meetingIdentifier(protocol, uid string) *wbxml.Node {
	if protocol == "16.0" || protocol == "16.1" {
		return wbxml.Str(wbxml.CalUID, uid)
	}
	return wbxml.Str(wbxml.EMGlobalObjId, globalObjID(uid))
}

// meetingMessageType names an invitation by its iCalendar SEQUENCE, the same rule the
// EWS MeetingRequestType follows: 0 is the first invitation, any later revision is a
// full update.
func meetingMessageType(sequence int32) string {
	if sequence > 0 {
		return meetingMsgFullUpdate
	}
	return meetingMsgInitial
}

// meetingResponseRequested reports whether the organizer wants a response, true
// unless PR_RESPONSE_REQUESTED is explicitly false, the rule the meeting workflow
// already answers with.
func meetingResponseRequested(pv mapi.PropertyValues) bool {
	if v, ok := pv.Get(mapi.PrResponseRequested); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return true
}

// meetingOrganizer names the organizer, preferring the representing identity and
// falling back to the sender, the order the meeting workflow reads it in.
func meetingOrganizer(pv mapi.PropertyValues) string {
	if addr := stringProp(pv, mapi.PrSentRepresentingSmtpAddress); addr != "" {
		return addr
	}
	return stringProp(pv, mapi.PrSenderSmtpAddress)
}

// globalObjIDClassID is the fixed 16-byte class id every Global Object ID starts
// with ([MS-ASEMAIL] 2.2.2.37).
var globalObjIDClassID = []byte{
	0x04, 0x00, 0x00, 0x00, 0x82, 0x00, 0xE0, 0x00,
	0x74, 0xC5, 0xB7, 0x10, 0x1A, 0x82, 0xE0, 0x08,
}

// globalObjIDVCalMarker and globalObjIDVersion mark the identifier as carrying an
// iCalendar UID rather than an Outlook one, so the device recovers the UID by reading
// the bytes after them.
var (
	globalObjIDVCalMarker = []byte("vCal-Uid")
	globalObjIDVersion    = []byte{0x01, 0x00, 0x00, 0x00}
)

// globalObjID wraps an iCalendar UID as the base64 Global Object ID MS-ASEMAIL asks
// for. The instance date is zero (the identifier names the series, not one
// occurrence) and the reserved bytes are zero, which is what the format reserves them
// for. An empty UID yields an empty value rather than an identifier naming nothing.
func globalObjID(uid string) string {
	if uid == "" {
		return ""
	}
	data := make([]byte, 0, len(globalObjIDVCalMarker)+len(globalObjIDVersion)+len(uid)+1)
	data = append(data, globalObjIDVCalMarker...)
	data = append(data, globalObjIDVersion...)
	data = append(data, uid...)
	data = append(data, 0x00)

	buf := make([]byte, 0, len(globalObjIDClassID)+24+len(data))
	buf = append(buf, globalObjIDClassID...)
	buf = append(buf, 0x00, 0x00, 0x00, 0x00) // INSTDATE: the whole series
	buf = binary.LittleEndian.AppendUint64(buf, mapi.UnixToNTTime(time.Now()))
	buf = append(buf, make([]byte, 8)...) // RESERVED
	// #nosec G115 -- a UID the store holds; its length is orders of magnitude below the field
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(data)))
	buf = append(buf, data...)
	return base64.StdEncoding.EncodeToString(buf)
}
