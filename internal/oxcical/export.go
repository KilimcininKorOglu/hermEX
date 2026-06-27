package oxcical

import (
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// Export renders an IPM.Appointment message as an iCalendar object. A recurring
// event preserved verbatim (PrIcalOriginal) is returned unchanged; otherwise a
// VEVENT is synthesized from the stored MAPI properties. Named properties are
// resolved through opt.Resolver with create=false, so a property never stored
// simply does not appear.
func Export(msg *oxcmail.Message, opt Options) ([]byte, error) {
	p := &msg.Props

	// A meeting response carries an iTIP METHOD:REPLY with organizer/attendee
	// identity; a plain appointment emits none of that, so its output is unchanged.
	partstat := responsePartStat(getStr(p, mapi.PrMessageClass))

	// A recurring event preserved verbatim is returned unchanged — but only when
	// rendering the appointment itself. A response synthesizes a REPLY below;
	// returning the preserved REQUEST verbatim would send the invitation back to the
	// organizer instead of the attendee's reply.
	if partstat == "" {
		if v, ok := p.Get(mapi.PrIcalOriginal); ok {
			if raw, ok := v.([]byte); ok && len(raw) > 0 {
				return raw, nil
			}
		}
	}

	named, err := namedTags(opt, false)
	if err != nil {
		return nil, err
	}
	uidTag, err := resolveOne(opt, nameICalUID, mapi.PtUnicode, false)
	if err != nil {
		return nil, err
	}

	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	b.add("VERSION:2.0")
	b.add("PRODID:-//hermEX//CalDAV//EN")
	if partstat != "" {
		b.add("METHOD:REPLY")
	}
	b.add("BEGIN:VEVENT")

	uid := ""
	if uidTag != 0 {
		uid = getStr(p, uidTag)
	}
	if uid == "" {
		uid = "hermex-event"
	}
	b.line("UID", uid)

	allDay := namedBool(p, named, mapi.NameAppointmentSubType)
	start, hasStart := namedTime(p, named, mapi.NameAppointmentStartWhole)
	end, hasEnd := namedTime(p, named, mapi.NameAppointmentEndWhole)

	// DTSTAMP is required (RFC 5545 §3.8.7.2); the start is a stable, deterministic
	// stamp for a synthesized event.
	if hasStart {
		b.add("DTSTAMP:" + formatICalUTC(start))
	}
	addLine(b, "SUMMARY", getStr(p, mapi.PrSubject))
	addLine(b, "DESCRIPTION", getStr(p, mapi.PrBody))
	addLine(b, "LOCATION", namedStr(p, named, mapi.NameAppointmentLocation))
	if hasStart {
		b.add(dtLine("DTSTART", start, allDay))
	}
	if hasEnd {
		b.add(dtLine("DTEND", end, allDay))
	}
	if busy, ok := namedLong(p, named, mapi.NameBusyStatus); ok {
		if busy == busyFree {
			b.add("TRANSP:TRANSPARENT")
		} else {
			b.add("TRANSP:OPAQUE")
		}
	}
	if v, ok := p.Get(mapi.PrSensitivity); ok {
		if s, ok := v.(int32); ok {
			if c := sensitivityClass(s); c != "" {
				b.add("CLASS:" + c)
			}
		}
	}
	if v, ok := p.Get(mapi.PrImportance); ok {
		if imp, ok := v.(int32); ok {
			b.line("PRIORITY", strconv.Itoa(int(importancePriority(imp))))
		}
	}
	if seq, ok := namedLong(p, named, mapi.NameAppointmentSequence); ok {
		b.line("SEQUENCE", strconv.Itoa(int(seq)))
	}
	if namedBool(p, named, mapi.NameReminderSet) {
		if delta, ok := namedLong(p, named, mapi.NameReminderDelta); ok {
			b.add("BEGIN:VALARM")
			b.add("ACTION:DISPLAY")
			b.add("TRIGGER:-PT" + strconv.Itoa(int(delta)) + "M")
			b.add("END:VALARM")
		}
	}

	// iTIP REPLY identity (RFC 5546 §3.2.3): the organizer being answered and the
	// one responding attendee, the response carried as the attendee's PARTSTAT.
	if partstat != "" {
		if v := mailtoParams(p, mapi.PrSentRepresentingSmtpAddress, mapi.PrSentRepresentingName, ""); v != "" {
			b.add("ORGANIZER" + v)
		}
		if v := mailtoParams(p, mapi.PrSenderSmtpAddress, mapi.PrSenderName, partstat); v != "" {
			b.add("ATTENDEE" + v)
		}
	} else if len(msg.Recipients) > 0 {
		// A meeting appointment re-emits its ORGANIZER and the full ATTENDEE list from
		// the stored recipients, so the invitee set round-trips for CalDAV clients and
		// stays visible to every protocol (single-data). A plain appointment has no
		// recipients and emits neither.
		if v := mailtoParams(p, mapi.PrSentRepresentingSmtpAddress, mapi.PrSentRepresentingName, ""); v != "" {
			b.add("ORGANIZER" + v)
		}
		for i := range msg.Recipients {
			if v := mailtoParams(&msg.Recipients[i], mapi.PrSmtpAddress, mapi.PrDisplayName, ""); v != "" {
				b.add("ATTENDEE" + v)
			}
		}
	}

	b.add("END:VEVENT")
	b.add("END:VCALENDAR")
	return b.buf.Bytes(), nil
}

// responsePartStat maps a meeting-response message class to the iCalendar PARTSTAT
// its REPLY reports — the inverse of import's meetingClass mapping, kept beside it
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
	s := ""
	if partstat != "" {
		s += ";PARTSTAT=" + partstat
	}
	if cn := getStr(p, nameTag); cn != "" {
		s += ";CN=\"" + strings.ReplaceAll(cn, "\"", "") + "\""
	}
	return s + ":mailto:" + addr
}

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
