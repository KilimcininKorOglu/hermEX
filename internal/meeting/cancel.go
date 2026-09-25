package meeting

import (
	"math"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// asfCanceled is the PidLidAppointmentStateFlags bit (MS-OXOCAL 2.2.1.10) that
// marks a meeting its organizer cancelled.
const asfCanceled int32 = 0x4

// mtgOutOfDate is the PidLidMeetingType value (MS-OXOCAL 2.2.6.5) of a meeting
// message a newer one superseded.
const mtgOutOfDate int32 = 0x00080000

// cancelClass is the message class a delivered METHOD:CANCEL is filed under.
const cancelClass = "IPM.Schedule.Meeting.Canceled"

// cancellation is a delivered meeting cancellation the mailbox is to apply.
type cancellation struct {
	id       int64
	sender   string
	subject  string
	uid      string
	seq      int
	at       time.Time
	instance bool
	tags     Tags
	seqTag   mapi.PropTag
}

// ProcessCancellation applies a delivered meeting cancellation to the attendee's
// calendar the way MS-OXOCAL 3.1.4.9.2 describes: the meeting, or the one instance
// the cancellation names, stays in the calendar marked cancelled and shown as free,
// and the user removes it. It reports whether the message was a cancellation it
// acted on, plus the error from the step that changes state.
//
// It acts only on a cancellation that has not been processed yet, in a mailbox
// that has not turned processing off, for a meeting the mailbox holds as an
// attendee, sent by that meeting's organizer: the envelope sender is compared
// with the organizer the stored meeting names, because anyone who knows a meeting
// UID could otherwise cancel it in another attendee's calendar. A cancellation
// older than the meeting the calendar holds is marked out of date and changes
// nothing, and an instance the calendar no longer has is not recreated. The
// delivery pass only runs for the Inbox, so a cancellation in Sent Items is never
// seen here.
func ProcessCancellation(st *objectstore.Store, sender string, messageID int64) (bool, error) {
	c, ok, err := readCancellation(st, sender, messageID)
	if err != nil || !ok {
		return false, err
	}
	appt, props, ok := c.meeting(st)
	if !ok {
		return false, nil
	}
	if c.seq < c.storedSequence(props) {
		return true, c.finish(st, mtgOutOfDate)
	}
	if err := c.apply(st, appt, props); err != nil {
		return true, err
	}
	return true, c.finish(st, 0)
}

// readCancellation reads a delivered message as a cancellation to process, or
// reports false when it is not one or must be left alone.
func readCancellation(st *objectstore.Store, sender string, messageID int64) (*cancellation, bool, error) {
	props, ics, ok, err := pendingCancellation(st, messageID)
	from := strings.ToLower(strings.TrimSpace(sender))
	if err != nil || !ok || from == "" {
		return nil, false, err
	}
	tags, err := ResolveTags(st)
	if err != nil {
		return nil, false, err
	}
	seqTag, err := longTag(st, mapi.NameAppointmentSequence)
	if err != nil {
		return nil, false, err
	}
	c := &cancellation{id: messageID, sender: from, subject: propStr(props, mapi.PrSubject), tags: tags, seqTag: seqTag}
	c.uid = strings.TrimSpace(icalLine(ics, "UID"))
	c.at, c.instance = oxcical.OccurrenceInstant(ics)
	c.seq = oxcical.Sequence(ics, c.instantRef())
	// The revision lands in a 32-bit named long, so a body carrying one it cannot
	// hold is not a cancellation this mailbox can record.
	inRange := c.seq >= 0 && c.seq <= math.MaxInt32
	return c, c.uid != "" && inRange, nil
}

// pendingCancellation reads a delivered cancellation not yet processed, in a
// mailbox that processes cancellations, with its iCalendar body.
func pendingCancellation(st *objectstore.Store, messageID int64) (mapi.PropertyValues, []byte, bool, error) {
	msg, err := st.OpenMessage(messageID)
	if err != nil || propStr(msg.Props, mapi.PrMessageClass) != cancelClass || boolVal(msg.Props, mapi.PrProcessed) {
		return nil, nil, false, nil
	}
	cfg, err := st.GetMeetingConfig()
	if err != nil || cfg.LeaveCancellationsUnprocessed {
		return nil, nil, false, err
	}
	ics, ok := inboxCalendarPart(st, messageID)
	if !ok || !strings.EqualFold(strings.TrimSpace(icalLine(ics, "METHOD")), "CANCEL") {
		return nil, nil, false, nil
	}
	return msg.Props, ics, true, nil
}

// instantRef is the instance the cancellation names, nil for the whole meeting.
func (c *cancellation) instantRef() *time.Time {
	if c.instance {
		return &c.at
	}
	return nil
}

// meeting finds the calendar item the cancellation is for, and reports false
// unless the mailbox holds it as an attendee of a meeting the sender organizes.
func (c *cancellation) meeting(st *objectstore.Store) (int64, mapi.PropertyValues, bool) {
	appt, ok := findCalendarByUID(st, c.tags.UID, c.uid)
	if !ok {
		return 0, nil, false
	}
	props, err := st.GetMessageProperties(appt, c.tags.State, c.seqTag, mapi.PrSentRepresentingSmtpAddress, mapi.PrIcalOriginal)
	if err != nil {
		st.LogSwallowedError("meeting.read-cancelled-meeting", err)
		return 0, nil, false
	}
	received := longVal(props, c.tags.State)&asfReceived != 0
	organizer := strings.TrimSpace(propStr(props, mapi.PrSentRepresentingSmtpAddress))
	return appt, props, received && strings.EqualFold(organizer, c.sender)
}

// storedSequence is the revision the calendar holds for what the cancellation
// names: the meeting's, and for an instance also the instance's own.
func (c *cancellation) storedSequence(props mapi.PropertyValues) int {
	seq := int(longVal(props, c.seqTag))
	if stored, ok := icalOf(props); ok {
		seq = max(seq, oxcical.Sequence(stored, nil))
		if c.instance {
			seq = max(seq, oxcical.Sequence(stored, &c.at))
		}
	}
	return seq
}

// apply marks what the cancellation names as cancelled: the one instance of a
// stored series, or the whole meeting. An instance the series no longer has
// stays gone.
func (c *cancellation) apply(st *objectstore.Store, appt int64, props mapi.PropertyValues) error {
	stored, isICal := icalOf(props)
	if c.instance && isICal {
		if cancelled, ok := oxcical.CancelInstance(stored, c.at); ok {
			if revised, ok := oxcical.SetSequence(cancelled, c.seq, &c.at); ok {
				cancelled = revised
			}
			return st.ModifyMessageProperties(appt, mapi.PropertyValues{{Tag: mapi.PrIcalOriginal, Value: cancelled}})
		}
		if _, series := oxcical.CancelOccurrence(stored, c.at); series {
			return nil
		}
	}
	update := mapi.PropertyValues{
		{Tag: c.tags.State, Value: longVal(props, c.tags.State) | asfCanceled},
		{Tag: c.tags.Busy, Value: busyFree},
		// #nosec G115 -- readCancellation refuses a revision outside the int32 range
		{Tag: c.seqTag, Value: int32(c.seq)},
	}
	if c.subject != "" {
		update.Set(mapi.PrSubject, c.subject)
	}
	if isICal {
		if revised, ok := oxcical.SetSequence(stored, c.seq, nil); ok {
			update.Set(mapi.PrIcalOriginal, revised)
		}
	}
	return st.ModifyMessageProperties(appt, update)
}

// finish marks the cancellation processed, and out of date when meetingType
// says so, so it is never applied a second time.
func (c *cancellation) finish(st *objectstore.Store, meetingType int32) error {
	update := mapi.PropertyValues{{Tag: mapi.PrProcessed, Value: true}}
	if meetingType != 0 {
		tag, err := longTag(st, mapi.NameMeetingType)
		if err != nil {
			return err
		}
		update.Set(tag, meetingType)
	}
	return st.ModifyMessageProperties(c.id, update)
}

// longTag resolves, allocating when absent, a PtLong named property.
func longTag(st *objectstore.Store, name mapi.PropertyName) (mapi.PropTag, error) {
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{name})
	if err != nil {
		return 0, err
	}
	return mapi.MakeTag(ids[0], mapi.PtLong), nil
}
