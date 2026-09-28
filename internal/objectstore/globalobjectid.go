package objectstore

import (
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
)

// stampGlobalObjectID gives a new appointment the PidLidGlobalObjectId it lacks
// ([MS-OXOCAL] 2.2.1.27). Every surface derives the object's identity from it: the
// iCalendar UID CalDAV and meeting mail carry, and the match of a meeting response
// to its meeting. A client that creates an appointment without one (an ActiveSync
// device, a MAPI client that leaves it to the server) would otherwise leave the
// object with no identity at all. An appointment that carries an iCalendar UID
// gets the id that UID names, so both stay one identity.
func (s *Store) stampGlobalObjectID(msg *oxcmail.Message) error {
	if !isAppointmentClass(msg.Props) {
		return nil
	}
	ids, err := s.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameGlobalObjectId, mapi.NameICalUID})
	if err != nil {
		return err
	}
	goidTag := mapi.MakeTag(ids[0], mapi.PtBinary)
	if ids[0] == 0 || msg.Props.Has(goidTag) {
		return nil
	}
	var goid []byte
	if v, ok := msg.Props.GetAnyCharset(mapi.MakeTag(ids[1], mapi.PtUnicode)); ok && ids[1] != 0 {
		uid, _ := v.(string)
		goid = oxcical.GlobalObjectID(uid)
	}
	if goid == nil {
		goid = oxcical.NewGlobalObjectID(time.Now())
	}
	msg.Props.Set(goidTag, goid)
	return nil
}

// isAppointmentClass reports whether a message is a calendar object.
func isAppointmentClass(props mapi.PropertyValues) bool {
	v, _ := props.GetAnyCharset(mapi.PrMessageClass)
	class, _ := v.(string)
	return class == "IPM.Appointment" || strings.HasPrefix(class, "IPM.Appointment.")
}
