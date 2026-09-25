package meeting

import (
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// Proposal is the span a counter proposal asks for, as UTC FILETIMEs. A response
// that proposes nothing carries a nil *Proposal.
type Proposal struct {
	Start, End uint64
}

// counterTags are the named properties a counter proposal travels in: on the
// response, the flag and the proposed span; on the organizer's meeting, the flag and
// the count of attendees with a proposal pending.
type counterTags struct {
	flag, start, end, number mapi.PropTag
}

func resolveCounterTags(st *objectstore.Store) (counterTags, error) {
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameAppointmentCounterProposal,
		mapi.NameAppointmentProposedStartWhole,
		mapi.NameAppointmentProposedEndWhole,
		mapi.NameAppointmentProposalNumber,
	})
	if err != nil {
		return counterTags{}, err
	}
	return counterTags{
		flag:   mapi.MakeTag(ids[0], mapi.PtBoolean),
		start:  mapi.MakeTag(ids[1], mapi.PtSysTime),
		end:    mapi.MakeTag(ids[2], mapi.PtSysTime),
		number: mapi.MakeTag(ids[3], mapi.PtLong),
	}, nil
}

// readProposal reads the proposal a delivered response carries: the counter flag
// and the proposed span its iCalendar import stored ([MS-OXCICAL] METHOD, DTSTART,
// DTEND). It returns nil for a response that proposes nothing.
func readProposal(st *objectstore.Store, messageID int64) (*Proposal, error) {
	ct, err := resolveCounterTags(st)
	if err != nil {
		return nil, err
	}
	props, err := st.GetMessageProperties(messageID, ct.flag, ct.start, ct.end)
	if err != nil {
		return nil, err
	}
	if flag, _ := props.Get(ct.flag); flag != true {
		return nil, nil
	}
	start, okStart := sysTime(props, ct.start)
	end, okEnd := sysTime(props, ct.end)
	if !okStart || !okEnd {
		return nil, nil
	}
	return &Proposal{Start: start, End: end}, nil
}

// sysTime reads a PtSysTime property.
func sysTime(props mapi.PropertyValues, tag mapi.PropTag) (uint64, bool) {
	v, ok := props.Get(tag)
	if !ok {
		return 0, false
	}
	t, ok := v.(uint64)
	return t, ok
}

// trackProposal records an attendee's counter proposal on the organizer's meeting
// the way [MS-OXOCAL] 3.1.4.8.5.3 has the organizer's client do it: the attendee's
// recipient row is marked as proposing with the proposed span, the meeting is
// flagged, and its proposal count rises on the attendee's first proposal. A later
// response that proposes nothing from an attendee who had proposed undoes it: the
// row is cleared, the count falls, and the flag drops once no proposal is pending.
func trackProposal(st *objectstore.Store, eventID, recipID int64, p *Proposal) error {
	row, err := st.GetRecipientProperties(recipID, mapi.PrRecipientProposed)
	if err != nil {
		return err
	}
	proposed, _ := row.Get(mapi.PrRecipientProposed)
	was := proposed == true
	if p == nil && !was {
		return nil
	}
	ct, err := resolveCounterTags(st)
	if err != nil {
		return err
	}
	if p == nil {
		if err := st.SetRecipientProperties(recipID, mapi.PropertyValues{{Tag: mapi.PrRecipientProposed, Value: false}}); err != nil {
			return err
		}
		return adjustProposals(st, ct, eventID, -1)
	}
	if err := st.SetRecipientProperties(recipID, mapi.PropertyValues{
		{Tag: mapi.PrRecipientProposed, Value: true},
		{Tag: mapi.PrRecipientProposedStartTime, Value: p.Start},
		{Tag: mapi.PrRecipientProposedEndTime, Value: p.End},
	}); err != nil {
		return err
	}
	delta := int32(0)
	if !was {
		delta = 1
	}
	return adjustProposals(st, ct, eventID, delta)
}

// adjustProposals moves the meeting's proposal count by delta, never below zero, and
// keeps the counter flag set exactly while a proposal is pending. The write goes
// through ModifyMessageProperties so the meeting's change number advances and a
// synchronizing client picks the proposal up.
func adjustProposals(st *objectstore.Store, ct counterTags, eventID int64, delta int32) error {
	props, err := st.GetMessageProperties(eventID, ct.number)
	if err != nil {
		return err
	}
	var n int32
	if v, ok := props.Get(ct.number); ok {
		n, _ = v.(int32)
	}
	n = max(n+delta, 0)
	return st.ModifyMessageProperties(eventID, mapi.PropertyValues{
		{Tag: ct.number, Value: n},
		{Tag: ct.flag, Value: n > 0},
	})
}
