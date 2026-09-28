package meeting

import (
	"errors"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
)

// Kind is what a meeting message is to the mailbox that holds it.
type Kind string

// The kinds of meeting message a reader tells apart ([MS-OXOCAL] 2.2.6): the
// invitation, an attendee's answer, an answer that proposes a new time, and the
// organizer's cancellation.
const (
	KindRequest      Kind = "request"
	KindResponse     Kind = "response"
	KindCounter      Kind = "counter"
	KindCancellation Kind = "cancellation"
)

// View is what a reader shows about a meeting message: its kind and the state that
// goes with that kind.
type View struct {
	Kind Kind
	// Response is, on a request, the answer the mailbox gave, and on a response or
	// a counter proposal, the answer it carries; 0 when there is none.
	Response int32
	// ResponseRequested reports whether a request asks for an answer.
	ResponseRequested bool
	// Organizer reports that the mailbox organizes the meeting a request is for:
	// the request is the mailbox's own invitation and has nothing to answer.
	Organizer bool
	// Proposal is the time a counter proposal asks for.
	Proposal *Proposal
	// Removable reports that what a cancellation calls off is still on the
	// calendar, marked canceled, for the reader to remove.
	Removable bool
}

// ErrNotCanceled is returned when a cancellation's meeting is not on the calendar
// marked canceled, so there is nothing for the reader to remove.
var ErrNotCanceled = errors.New("meeting: nothing the cancellation calls off is on the calendar")

// responseClassPrefix starts the class of every meeting response, lower-cased.
const responseClassPrefix = "ipm.schedule.meeting.resp."

// isClass reports whether the lower-cased class is base or a class derived from it.
func isClass(class, base string) bool {
	base = strings.ToLower(base)
	return class == base || strings.HasPrefix(class, base+".")
}

// Describe reads what a reader shows about the meeting message at messageID, whose
// calendar part is ics. ok is false when the message is not a meeting message.
func Describe(st *objectstore.Store, messageID int64, ics []byte) (View, bool, error) {
	tags, err := ResolveTags(st)
	if err != nil {
		return View{}, false, err
	}
	msg, err := st.OpenMessage(messageID)
	if err != nil {
		return View{}, false, ErrRequestNotFound
	}
	class := strings.ToLower(propStr(msg.Props, mapi.PrMessageClass))
	switch {
	case isClass(class, requestClass):
		v, err := describeRequest(st, msg, tags)
		return v, true, err
	case strings.HasPrefix(class, responseClassPrefix):
		v, err := describeResponse(st, messageID, class)
		return v, true, err
	case isClass(class, cancelClass):
		_, removable := findCanceled(st, tags, ics)
		return View{Kind: KindCancellation, Removable: removable}, true, nil
	}
	return View{}, false, nil
}

// describeRequest reads a request's standing: whether the organizer asks for an
// answer, whether the mailbox organizes the meeting itself, and the answer the
// mailbox gave. The answer is read from the meeting on the calendar, which a
// response from any protocol updates, and from the request's own stamp when the
// calendar holds no meeting, as after a decline.
func describeRequest(st *objectstore.Store, req *oxcmail.Message, tags Tags) (View, error) {
	v := View{Kind: KindRequest, ResponseRequested: responseRequested(req.Props), Response: answer(longVal(req.Props, tags.Resp))}
	appt, ok := findCalendarByUID(st, tags.UID, uidOf(req.Props, tags))
	if !ok {
		return v, nil
	}
	props, err := st.GetMessageProperties(appt, tags.State, tags.Resp)
	if err != nil {
		return v, err
	}
	if longVal(props, tags.State)&asfReceived == 0 {
		return View{Kind: KindRequest, Organizer: true}, nil
	}
	if r := answer(longVal(props, tags.Resp)); r != 0 {
		v.Response = r
	}
	return v, nil
}

// answer keeps a response status that is an attendee's answer: accepted, tentative
// or declined. Every other status, such as "not responded", is no answer.
func answer(status int32) int32 {
	switch status {
	case ResponseAccepted, ResponseTentative, ResponseDeclined:
		return status
	}
	return 0
}

// describeResponse reads the answer a response carries, from its class, and the
// time it proposes when it is a counter proposal.
func describeResponse(st *objectstore.Store, messageID int64, class string) (View, error) {
	v := View{Kind: KindResponse, Response: classResponse(class)}
	p, err := readProposal(st, messageID)
	if err != nil || p == nil {
		return v, err
	}
	v.Kind, v.Proposal = KindCounter, p
	return v, nil
}

// classResponse maps a lower-cased meeting response class to the answer it names.
func classResponse(class string) int32 {
	switch strings.TrimPrefix(class, responseClassPrefix) {
	case "pos":
		return ResponseAccepted
	case "tent":
		return ResponseTentative
	case "neg":
		return ResponseDeclined
	}
	return 0
}

// canceledItem is what a cancellation calls off as the calendar holds it: the
// meeting at appt, or, when body is set, the instance at instance of the series
// body stores.
type canceledItem struct {
	appt     int64
	instance time.Time
	body     []byte
}

// findCanceled finds on the calendar what the cancellation ics calls off and
// reports whether it is there marked canceled, in a meeting the mailbox was
// invited to: the meeting, or the one instance of a series the cancellation names.
// Only the processing of a cancellation its organizer sent marks a meeting
// canceled, so a cancellation from anyone else, or one older than the meeting,
// finds nothing.
func findCanceled(st *objectstore.Store, tags Tags, ics []byte) (canceledItem, bool) {
	appt, ok := findCalendarByUID(st, tags.UID, strings.TrimSpace(icalLine(ics, "UID")))
	if !ok {
		return canceledItem{}, false
	}
	props, err := st.GetMessageProperties(appt, tags.State, mapi.PrIcalOriginal)
	if err != nil {
		st.LogSwallowedError("meeting.read-canceled-meeting", err)
		return canceledItem{}, false
	}
	state := longVal(props, tags.State)
	if state&asfReceived == 0 {
		return canceledItem{}, false
	}
	if at, single := oxcical.OccurrenceInstant(ics); single {
		body, _ := icalOf(props)
		if cancelled, series := oxcical.InstanceCancelled(body, at); series {
			return canceledItem{appt: appt, instance: at, body: body}, cancelled
		}
	}
	return canceledItem{appt: appt}, state&asfCanceled != 0
}

// RemoveCanceled takes off the calendar what the cancellation at messageID, whose
// calendar part is ics, called off: the meeting, or the one instance of a series.
// It is the attendee removing a meeting its organizer canceled ([MS-OXOCAL]
// 3.1.4.9.2), so only what the calendar holds as canceled is removed, and
// ErrNotCanceled reports there is nothing.
func RemoveCanceled(st *objectstore.Store, messageID int64, ics []byte) error {
	tags, err := ResolveTags(st)
	if err != nil {
		return err
	}
	msg, err := st.OpenMessage(messageID)
	if err != nil {
		return ErrRequestNotFound
	}
	if !isClass(strings.ToLower(propStr(msg.Props, mapi.PrMessageClass)), cancelClass) {
		return ErrNotCanceled
	}
	item, ok := findCanceled(st, tags, ics)
	if !ok {
		return ErrNotCanceled
	}
	if item.body == nil {
		return st.DeleteObject(item.appt)
	}
	trimmed, ok := oxcical.CancelOccurrence(item.body, item.instance)
	if !ok {
		return ErrNotCanceled
	}
	return st.ModifyMessageProperties(item.appt, withRecurrence(st, mapi.PropertyValues{
		{Tag: mapi.PrIcalOriginal, Value: trimmed},
	}, trimmed))
}
