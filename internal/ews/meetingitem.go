package ews

import (
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// meetingTagSet holds the appointment named-property tags one mailbox resolved, the
// values a delivered invitation carries beyond its mail headers.
type meetingTagSet struct {
	start, end, allDay, busy, uid, sequence mapi.PropTag
}

// meetingTags resolves the appointment named properties an invitation is rendered
// from. It never allocates: a mailbox that has never stored an appointment has no
// invitation to render either, and ids[i] == 0 simply reads as an absent property.
func meetingTags(st *objectstore.Store) (meetingTagSet, error) {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole, // 0
		mapi.NameAppointmentEndWhole,   // 1
		mapi.NameAppointmentSubType,    // 2
		mapi.NameBusyStatus,            // 3
		mapi.NameICalUID,               // 4
		mapi.NameAppointmentSequence,   // 5
	})
	if err != nil {
		return meetingTagSet{}, err
	}
	return meetingTagSet{
		start:    mapi.MakeTag(ids[0], mapi.PtSysTime),
		end:      mapi.MakeTag(ids[1], mapi.PtSysTime),
		allDay:   mapi.MakeTag(ids[2], mapi.PtBoolean),
		busy:     mapi.MakeTag(ids[3], mapi.PtLong),
		uid:      mapi.MakeTag(ids[4], mapi.PtUnicode),
		sequence: mapi.MakeTag(ids[5], mapi.PtLong),
	}, nil
}

// meetingLocationTag resolves the appointment location separately, because it is the
// one field the invitation carries under a second named property.
func meetingLocationTag(st *objectstore.Store) mapi.PropTag {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameAppointmentLocation})
	if err != nil {
		return 0
	}
	return mapi.MakeTag(ids[0], mapi.PtUnicode)
}

// meetingMeta builds the meeting half of a <t:MeetingRequest> from the stored
// invitation. Every value is read off the message that was delivered, so the answer
// does not depend on what the mailbox's calendar already holds.
func meetingMeta(st *objectstore.Store, msg *oxcmail.Message) oxews.MeetingMeta {
	tags, err := meetingTags(st)
	if err != nil {
		st.LogSwallowedError("ews.meeting-tags", err)
		return oxews.MeetingMeta{RequestType: oxews.MeetingTypeNew, ResponseRequested: true}
	}
	props := msg.Props
	return oxews.MeetingMeta{
		Start:             ntTime(props, tags.start),
		End:               ntTime(props, tags.end),
		AllDay:            boolProp(props, tags.allDay),
		Location:          strProp(props, meetingLocationTag(st)),
		UID:               strProp(props, tags.uid),
		BusyStatus:        busyTypeName(longProp(props, tags.busy)),
		ResponseRequested: responseRequested(props),
		RequestType:       meetingRequestType(longProp(props, tags.sequence)),
		Organizer:         organizerMailbox(props),
	}
}

// meetingRequestType names an invitation by its iCalendar SEQUENCE: 0 is the first
// invitation, and any later revision is a full update (RFC 5546 increments SEQUENCE
// on a significant change). The answer comes from the message itself, which is where
// Exchange takes it from too, rather than from the mailbox's own calendar.
func meetingRequestType(sequence int32) string {
	if sequence > 0 {
		return oxews.MeetingTypeFullUpdate
	}
	return oxews.MeetingTypeNew
}

// responseRequested reports whether the organizer wants a response, true unless
// PR_RESPONSE_REQUESTED is explicitly false. It is the rule the meeting workflow
// already answers with, so a client and the organizer notification agree.
func responseRequested(props mapi.PropertyValues) bool {
	if v, ok := props.Get(mapi.PrResponseRequested); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return true
}

// organizerMailbox names the meeting's organizer, preferring the representing
// identity and falling back to the sender, the same order the meeting workflow reads
// it in when it addresses an iTIP REPLY.
func organizerMailbox(props mapi.PropertyValues) *oxews.Mailbox {
	name := strProp(props, mapi.PrSentRepresentingName)
	addr := strProp(props, mapi.PrSentRepresentingSmtpAddress)
	if addr == "" {
		name = strProp(props, mapi.PrSenderName)
		addr = strProp(props, mapi.PrSenderSmtpAddress)
	}
	if name == "" && addr == "" {
		return nil
	}
	return &oxews.Mailbox{Name: name, EmailAddress: addr}
}

// boolProp reads a PtBoolean property, false when absent.
func boolProp(props mapi.PropertyValues, tag mapi.PropTag) bool {
	if v, ok := props.Get(tag); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// longProp reads a PtLong property as an int32, 0 when absent.
func longProp(props mapi.PropertyValues, tag mapi.PropTag) int32 {
	if v, ok := props.Get(tag); ok {
		switch n := v.(type) {
		case int32:
			return n
		case int64:
			// #nosec G115 -- the property was stored as a PtLong
			return int32(n)
		}
	}
	return 0
}
