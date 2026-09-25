package webmail2api

import (
	"errors"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// errNoSuchEvent reports an update aimed at an id that holds no appointment.
var errNoSuchEvent = errors.New("webmail2api: no such event")

// updateEventInPlace rewrites a stored appointment from the editor's fields and
// keeps it the same object: the message id, the iCalendar UID that is the
// meeting's identity, the attendees' response statuses, and every property and
// attachment another client stored. Only the properties the iCalendar import
// manages are replaced, and those the edit no longer sets are removed. A series
// keeps its cancelled and moved instances while its first start and rule stay
// the same. The appointment moves when the editor names another calendar. It
// returns the event's iCalendar UID.
func updateEventInPlace(st *objectstore.Store, id int64, in eventJSON) (string, error) {
	stored, err := st.OpenMessage(id)
	if errors.Is(err, objectstore.ErrNotFound) || (err == nil && !isAppointment(stored.Props)) {
		return "", errNoSuchEvent
	}
	if err != nil {
		return "", err
	}
	opt := oxcical.Options{Resolver: st.GetNamedPropIDs}
	in.UID = storedICalUID(st, stored.Props)
	ics := buildICal(in)
	if raw, ok := stored.Props.Get(mapi.PrIcalOriginal); ok {
		if b, ok := raw.([]byte); ok {
			ics = oxcical.CarryExceptions(b, ics)
		}
	}
	if err := rewriteEvent(st, id, ics, opt); err != nil {
		return "", err
	}
	if err := applyBusyStatus(st, id, in); err != nil {
		return "", err
	}
	if err := st.SetCategories(id, in.Categories); err != nil {
		return "", err
	}
	return in.UID, moveToCalendar(st, id, calendarFolderID(in.CalendarID))
}

// rewriteEvent imports ics and writes it over the stored appointment: the managed
// properties it sets, the removal of those it does not, and its attendees.
func rewriteEvent(st *objectstore.Store, id int64, ics []byte, opt oxcical.Options) error {
	msg, err := oxcical.Import(ics, opt)
	if err != nil {
		return err
	}
	managed, err := oxcical.ManagedTags(opt)
	if err != nil {
		return err
	}
	if err := st.ModifyMessageProperties(id, msg.Props, absentTags(managed, msg.Props)...); err != nil {
		return err
	}
	return st.ReplaceRecipients(id, msg.Recipients)
}

// isAppointment reports whether a stored item is a calendar appointment.
func isAppointment(props mapi.PropertyValues) bool {
	return strings.HasPrefix(strings.ToUpper(propStr(props, mapi.PrMessageClass)), "IPM.APPOINTMENT")
}

// storedICalUID returns the iCalendar UID an appointment was stored under, or a
// fresh one for an appointment that has none.
func storedICalUID(st *objectstore.Store, props mapi.PropertyValues) string {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameICalUID})
	if err == nil && len(ids) == 1 && ids[0] != 0 {
		if uid := propStr(props, mapi.MakeTag(ids[0], mapi.PtUnicode)); uid != "" {
			return uid
		}
	}
	return uidOrGenerated("")
}

// absentTags returns the tags of managed that props does not set.
func absentTags(managed []mapi.PropTag, props mapi.PropertyValues) []mapi.PropTag {
	var out []mapi.PropTag
	for _, tag := range managed {
		if !props.Has(tag) {
			out = append(out, tag)
		}
	}
	return out
}

// moveToCalendar moves an appointment into dest when it lives elsewhere, keeping
// its message id.
func moveToCalendar(st *objectstore.Store, id, dest int64) error {
	fid, err := st.MessageFolder(id)
	if err != nil || fid == dest {
		return err
	}
	_, err = st.MoveMessageImport(fid, id, dest, id)
	return err
}

// applyBusyStatus writes the busy status the editor chose. It is set directly as a
// named property because the iCalendar TRANSP and STATUS the import reads cannot
// express all of its values (out of office has no iCalendar form), so the editor's
// choice overrides the import default.
func applyBusyStatus(st *objectstore.Store, id int64, e eventJSON) error {
	if e.BusyStatus == nil || !fitsMAPILong(*e.BusyStatus) {
		return nil
	}
	tag, err := busyStatusTag(st, true)
	if err != nil || tag == 0 {
		return err
	}
	var props mapi.PropertyValues
	// #nosec G115 -- the fitsMAPILong guard above refuses a value the property cannot carry
	props.Set(tag, int32(*e.BusyStatus))
	return st.SetMessageProperties(id, props)
}
