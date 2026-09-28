package meeting

import (
	"errors"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/relay"
)

// ErrNotRecurring is returned for a response to one instance of a meeting that is
// not a series.
var ErrNotRecurring = errors.New("meeting: the meeting is not recurring")

// ErrNoInstance is returned for a response to an instance the series does not have.
var ErrNoInstance = errors.New("meeting: the series has no such instance")

// respondToInstance records a response to the one occurrence of a series that
// starts at at: accepting folds that occurrence into the stored series as an
// exception, declining takes it out of the series, and the organizer is told about
// that occurrence alone. The series keeps its own response, and the request mail
// stays, because it still stands for every other occurrence.
func respondToInstance(st *objectstore.Store, accounts directory.Accounts, spool *relay.Spool, who identity, req *oxcmail.Message, response int32, reply Reply) (int64, error) {
	view, err := instanceView(st, req, reply.Instance)
	if err != nil {
		return 0, err
	}
	tags, err := ResolveTags(st)
	if err != nil {
		return 0, err
	}
	var calendarID int64
	if response == ResponseDeclined {
		err = removeAppointment(st, view, tags)
	} else {
		calendarID, err = file(st, view, tags, response, mapi.UnixToNTTime(time.Now()))
	}
	if err != nil {
		return 0, err
	}
	if reply.Send {
		if err := notifyOrganizer(st, accounts, spool, who, view, response, reply); err != nil {
			return 0, err
		}
	}
	return calendarID, nil
}

// instanceView narrows a series to its occurrence at: the request's properties with
// the occurrence's own iCalendar, span and identity in place of the series', and
// the original start recorded as the replace time, which is what makes a response
// name the occurrence in RECURRENCE-ID ([MS-OXCICAL] RECURRENCE-ID).
func instanceView(st *objectstore.Store, req *oxcmail.Message, at time.Time) (*oxcmail.Message, error) {
	series, ok := icalOf(req.Props)
	if !ok {
		return nil, ErrNotRecurring
	}
	if _, _, _, recurring := oxcical.ParseRecurrence(series); !recurring {
		return nil, ErrNotRecurring
	}
	body, ok := oxcical.InstanceBody(series, at, "REQUEST", oxcical.Sequence(series, &at))
	if !ok {
		return nil, ErrNoInstance
	}
	occ, err := oxcical.Import(body, oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return nil, err
	}
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameExceptionReplaceTime, mapi.NameRecurring, mapi.NameAppointmentRecur,
	})
	if err != nil {
		return nil, err
	}
	props := append(mapi.PropertyValues(nil), req.Props...)
	props.Remove(mapi.MakeTag(ids[1], mapi.PtBoolean))
	props.Remove(mapi.MakeTag(ids[2], mapi.PtBinary))
	for _, pv := range occ.Props {
		if pv.Tag != mapi.PrMessageClass {
			props.Set(pv.Tag, pv.Value)
		}
	}
	props.Set(mapi.MakeTag(ids[0], mapi.PtSysTime), mapi.UnixToNTTime(at))
	return &oxcmail.Message{Props: props, Recipients: req.Recipients}, nil
}
