package oxcical

import (
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// Export renders a calendar object or a meeting message as an iCalendar object. A
// plain appointment preserved verbatim (PrIcalOriginal) is returned unchanged;
// otherwise a VEVENT is synthesized from the stored MAPI properties. A meeting
// message carries the iTIP METHOD its class names ([MS-OXCICAL] METHOD table).
// Named properties are resolved through opt.Resolver with create=false, so a
// property never stored simply does not appear.
func Export(msg *oxcmail.Message, opt Options) ([]byte, error) {
	p := &msg.Props
	class := getStr(p, mapi.PrMessageClass)
	if classMethod(class) == "" {
		if raw, ok := storedICal(p); ok {
			return raw, nil
		}
	}

	named, err := resolveFields(opt, exportFields, false)
	if err != nil {
		return nil, err
	}
	uidTag, err := resolveOne(opt, nameICalUID, mapi.PtUnicode, false)
	if err != nil {
		return nil, err
	}
	e := newEventExport(msg, named, uidTag, class)
	if raw, ok := e.verbatimMeeting(); ok {
		return raw, nil
	}
	return e.render(), nil
}

// storedICal returns the iCalendar a recurring event preserved unchanged.
func storedICal(p *mapi.PropertyValues) ([]byte, bool) {
	v, ok := p.Get(mapi.PrIcalOriginal)
	if !ok {
		return nil, false
	}
	raw, ok := v.([]byte)
	if !ok || len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

// classMethod maps a meeting message class to the iTIP METHOD it exports as
// ([MS-OXCICAL] METHOD table): REQUEST, CANCEL, or REPLY for any response. A
// counter proposal is a response too, told apart by a property (see
// newEventExport). Every other class is a plain calendar object and exports none.
func classMethod(class string) string {
	switch {
	case responsePartStat(class) != "":
		return "REPLY"
	case class == "IPM.Schedule.Meeting.Request":
		return "REQUEST"
	case class == "IPM.Schedule.Meeting.Canceled":
		return "CANCEL"
	}
	return ""
}

// eventUID returns the stored iCalendar UID, else the one the MAPI global object id
// exports as, else a constant fallback so the VEVENT always carries the property
// RFC 5545 §3.8.4.7 requires.
func eventUID(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, uidTag mapi.PropTag) string {
	if uidTag != 0 {
		if uid := getStr(p, uidTag); uid != "" {
			return uid
		}
	}
	if uid := globalObjectUID(namedBytes(p, named, mapi.NameGlobalObjectId)); uid != "" {
		return uid
	}
	return "hermex-event"
}

// exportClassification emits how the event is filed: whether it takes time, how
// private it is, how urgent, and which revision it is.
func exportClassification(b *builder, p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag) {
	if busy, ok := namedLong(p, named, mapi.NameBusyStatus); ok {
		b.add(transpLine(busy))
	}
	if s, ok := propInt32(p, mapi.PrSensitivity); ok {
		if c := sensitivityClass(s); c != "" {
			b.add("CLASS:" + c)
		}
	}
	if imp, ok := propInt32(p, mapi.PrImportance); ok {
		b.line("PRIORITY", strconv.Itoa(int(importancePriority(imp))))
	}
	if seq, ok := namedLong(p, named, mapi.NameAppointmentSequence); ok {
		b.line("SEQUENCE", strconv.Itoa(int(seq)))
	}
}

// transpLine renders TRANSP, which says whether the event takes the attendee's
// time, the thing a VFREEBUSY reader aggregates. Working elsewhere does not, so
// exporting it as opaque would block a whole home office day for every CalDAV
// client.
func transpLine(busy int32) string {
	if mapi.BusyStatusOccupies(busy) {
		return "TRANSP:OPAQUE"
	}
	return "TRANSP:TRANSPARENT"
}

// exportAlarm emits the display reminder a stored delta asks for.
func exportAlarm(b *builder, p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag) {
	if !namedBool(p, named, mapi.NameReminderSet) {
		return
	}
	delta, ok := namedLong(p, named, mapi.NameReminderDelta)
	if !ok {
		return
	}
	b.add("BEGIN:VALARM")
	b.add("ACTION:DISPLAY")
	b.add("TRIGGER:-PT" + strconv.Itoa(int(delta)) + "M")
	b.add("END:VALARM")
}

// exportIdentity emits the ORGANIZER and ATTENDEE lines. For a response that is the
// iTIP REPLY identity (RFC 5546 §3.2.3): the organizer being answered and the one
// responding attendee, the response carried as the attendee's PARTSTAT. For a
// meeting appointment it is the organizer plus the full stored recipient list, so
// the invitee set round-trips for CalDAV clients and stays visible to every
// protocol (single-data). A plain appointment has no recipients and emits neither.
//
// A response reads its ATTENDEE from the representing identity, because that is who
// the message is from, and its ORGANIZER from the message's one recipient, because a
// response is addressed to the organizer it answers. Reading the ORGANIZER from the
// representing identity instead made the responder's own address the organizer on an
// inbound response, and forced a produced response to store the organizer as the
// representing identity, which then became the mail's From header.
func exportIdentity(b *builder, msg *oxcmail.Message, partstat string) {
	p := &msg.Props
	if partstat != "" {
		exportReplyIdentity(b, msg, partstat)
		return
	}
	if len(msg.Recipients) == 0 {
		return
	}
	addParams(b, "ORGANIZER", mailtoParams(p, mapi.PrSentRepresentingSmtpAddress, mapi.PrSentRepresentingName, ""))
	for i := range msg.Recipients {
		if params := mailtoParams(&msg.Recipients[i], mapi.PrSmtpAddress, mapi.PrDisplayName, ""); params != "" {
			b.add("ATTENDEE" + attendeeRole(&msg.Recipients[i]) + params)
		}
	}
}

// attendeeRole renders the ROLE parameter of an exported attendee from its
// recipient type ([MS-OXCICAL] 2.1.3.1.1.20.2.4): OPT-PARTICIPANT for Cc,
// NON-PARTICIPANT for Bcc, and nothing for a required (To) attendee.
func attendeeRole(p *mapi.PropertyValues) string {
	t, _ := propInt32(p, mapi.PrRecipientType)
	switch t {
	case mapi.RecipCc:
		return ";ROLE=OPT-PARTICIPANT"
	case mapi.RecipBcc:
		return ";ROLE=NON-PARTICIPANT"
	}
	return ""
}

// exportReplyIdentity emits an iTIP REPLY's ORGANIZER and ATTENDEE. The responder is
// the representing identity, falling back to the sender for a message that stores
// only one. The organizer is the first recipient.
func exportReplyIdentity(b *builder, msg *oxcmail.Message, partstat string) {
	p := &msg.Props
	attendee := mailtoParams(p, mapi.PrSentRepresentingSmtpAddress, mapi.PrSentRepresentingName, partstat)
	if attendee == "" {
		attendee = mailtoParams(p, mapi.PrSenderSmtpAddress, mapi.PrSenderName, partstat)
	}
	if len(msg.Recipients) > 0 {
		addParams(b, "ORGANIZER", mailtoParams(&msg.Recipients[0], mapi.PrSmtpAddress, mapi.PrDisplayName, ""))
	}
	addParams(b, "ATTENDEE", attendee)
}

// addParams emits a property line only when the identity rendered to something.
func addParams(b *builder, name, params string) {
	if params != "" {
		b.add(name + params)
	}
}

// propInt32 returns a PtLong property's value (ok false when absent or another type).
func propInt32(p *mapi.PropertyValues, tag mapi.PropTag) (int32, bool) {
	if v, ok := p.Get(tag); ok {
		if n, ok := v.(int32); ok {
			return n, true
		}
	}
	return 0, false
}

// responsePartStat maps a meeting-response message class to the iCalendar PARTSTAT
// its REPLY reports, the inverse of import's meetingClass mapping, kept beside it
// so the two directions cannot drift. A non-response class yields "" (no METHOD,
// organizer, or attendee is emitted, leaving a plain appointment's output as is).
func responsePartStat(class string) string {
	switch class {
	case "IPM.Schedule.Meeting.Resp.Pos":
		return "ACCEPTED"
	case "IPM.Schedule.Meeting.Resp.Neg":
		return "DECLINED"
	case "IPM.Schedule.Meeting.Resp.Tent":
		return "TENTATIVE"
	}
	return ""
}

// mailtoParams renders the parameters and mailto value of an ORGANIZER/ATTENDEE
// line from a stored identity: an optional PARTSTAT, an optional CN from the
// display name, and ":mailto:addr". It returns "" when no address is stored, so the
// caller emits nothing.
func mailtoParams(p *mapi.PropertyValues, smtpTag, nameTag mapi.PropTag, partstat string) string {
	addr := getStr(p, smtpTag)
	if addr == "" {
		return ""
	}
	addr = icalParamSafe.Replace(addr)
	s := ""
	if partstat != "" {
		s += ";PARTSTAT=" + partstat
	}
	if cn := icalParamSafe.Replace(getStr(p, nameTag)); cn != "" {
		s += ";CN=\"" + cn + "\""
	}
	return s + ":mailto:" + addr
}

// icalParamSafe cleans a stored identity for a content line. This line is written
// unescaped (it is parameters and a URI, not a TEXT value), so a line break in the
// display name or address would end the line and start a property of the client's
// choosing, and a double quote would close the quoted parameter early. The values
// are stored MAPI strings any protocol can write, so neither is trustworthy.
var icalParamSafe = strings.NewReplacer("\r", "", "\n", "", `"`, "")

// dtLine renders a DTSTART/DTEND line: a date-only value for an all-day event,
// else a UTC date-time.
func dtLine(name string, t time.Time, allDay bool) string {
	if allDay {
		return name + ";VALUE=DATE:" + formatICalDate(t)
	}
	return name + ":" + formatICalUTC(t)
}

// sensitivityClass maps PR_SENSITIVITY to an iCalendar CLASS (none ⇒ "" so PUBLIC
// is left implicit).
func sensitivityClass(s int32) string {
	switch s {
	case mapi.SensitivityPrivate, mapi.SensitivityPersonal:
		return "PRIVATE"
	case mapi.SensitivityConfidential:
		return "CONFIDENTIAL"
	}
	return ""
}

// importancePriority maps PR_IMPORTANCE to an iCalendar PRIORITY (high ⇒ 1,
// normal ⇒ 5, low ⇒ 9).
func importancePriority(imp int32) int32 {
	switch imp {
	case mapi.ImportanceHigh:
		return 1
	case mapi.ImportanceLow:
		return 9
	default:
		return 5
	}
}

// getStr returns a string-valued property, or "".
func getStr(p *mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := p.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// namedStr returns a named string-valued property, or "".
func namedStr(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName) string {
	if tag, ok := named[name]; ok {
		return getStr(p, tag)
	}
	return ""
}

// namedBool returns a named PtBoolean property's value (false when absent).
func namedBool(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName) bool {
	if tag, ok := named[name]; ok {
		if v, ok := p.Get(tag); ok {
			if b, ok := v.(bool); ok {
				return b
			}
		}
	}
	return false
}

// namedTime returns a named PtSysTime property as a UTC time (ok false when absent).
func namedTime(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName) (time.Time, bool) {
	if tag, ok := named[name]; ok {
		if v, ok := p.Get(tag); ok {
			if nt, ok := v.(uint64); ok {
				return mapi.NTTimeToUnix(nt).UTC(), true
			}
		}
	}
	return time.Time{}, false
}

// namedLong returns a named PtLong property (ok false when absent).
func namedLong(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName) (int32, bool) {
	if tag, ok := named[name]; ok {
		if v, ok := p.Get(tag); ok {
			if n, ok := v.(int32); ok {
				return n, true
			}
		}
	}
	return 0, false
}

// addLine emits a simple TEXT line only when the value is non-empty.
func addLine(b *builder, name, value string) {
	if value != "" {
		b.line(name, value)
	}
}
