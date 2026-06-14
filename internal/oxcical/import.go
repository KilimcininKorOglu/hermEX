package oxcical

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

var errNoEvent = errors.New("oxcical: no VEVENT in calendar")

// iCalendar busy/transparency maps to PidLidBusyStatus values.
const (
	busyFree      int32 = 0
	busyTentative int32 = 1
	busyBusy      int32 = 2
)

// Import parses an iCalendar object into an IPM.Appointment message. A
// non-recurring event is synthesized into MAPI properties; a recurring event
// (carrying RRULE or RECURRENCE-ID) is preserved verbatim in PrIcalOriginal and
// gets only the minimal listing properties, because v1 does not synthesize the
// binary recurrence pattern. Named properties are resolved through opt.Resolver.
func Import(raw []byte, opt Options) (*oxcmail.Message, error) {
	cal, err := parseICal(raw)
	if err != nil {
		return nil, err
	}
	vev := cal.sub("VEVENT")
	if vev == nil {
		return nil, errNoEvent
	}

	named, err := namedTags(opt, true)
	if err != nil {
		return nil, err
	}
	uidTag, err := resolveOne(opt, nameICalUID, mapi.PtUnicode, true)
	if err != nil {
		return nil, err
	}

	msg := &oxcmail.Message{}
	p := &msg.Props
	p.Set(mapi.PrMessageClass, "IPM.Appointment")

	uid := strings.TrimSpace(vev.propText("UID"))
	if uid == "" {
		uid = generatedUID(vev)
	}
	if uidTag != 0 {
		p.Set(uidTag, uid)
	}
	setIf(p, mapi.PrSubject, vev.propText("SUMMARY"))

	// Recurring events round-trip verbatim; store only what listing needs.
	if vev.prop("RRULE") != nil || vev.prop("RECURRENCE-ID") != nil {
		p.Set(mapi.PrIcalOriginal, append([]byte(nil), raw...))
		if l := vev.prop("DTSTART"); l != nil {
			if t, _, ok := parseICalTime(l); ok {
				setNamedTime(p, named, mapi.NameAppointmentStartWhole, t)
			}
		}
		return msg, nil
	}

	// Non-recurring: full property synthesis.
	setIf(p, mapi.PrBody, vev.propText("DESCRIPTION"))
	setNamedStr(p, named, mapi.NameAppointmentLocation, vev.propText("LOCATION"))

	if l := vev.prop("DTSTART"); l != nil {
		if start, allDay, ok := parseICalTime(l); ok {
			setNamedTime(p, named, mapi.NameAppointmentStartWhole, start)
			if end, ok := eventEnd(vev, start, allDay); ok {
				setNamedTime(p, named, mapi.NameAppointmentEndWhole, end)
			}
			if allDay {
				setNamedBool(p, named, mapi.NameAppointmentSubType, true)
			}
		}
	}

	setNamedLong(p, named, mapi.NameBusyStatus, busyStatus(vev))
	if c := vev.propText("CLASS"); c != "" {
		p.Set(mapi.PrSensitivity, classSensitivity(c))
	}
	if imp, ok := priorityImportance(vev.propText("PRIORITY")); ok {
		p.Set(mapi.PrImportance, imp)
	}
	if n, err := strconv.Atoi(strings.TrimSpace(vev.propText("SEQUENCE"))); err == nil {
		setNamedLong(p, named, mapi.NameAppointmentSequence, int32(n))
	}
	if al := vev.sub("VALARM"); al != nil {
		if mins, ok := alarmMinutes(al); ok {
			setNamedBool(p, named, mapi.NameReminderSet, true)
			setNamedLong(p, named, mapi.NameReminderDelta, mins)
		}
	}
	return msg, nil
}

// eventEnd resolves the event end from DTEND, else DTSTART+DURATION, else (for an
// all-day event) one day after the start, else the start itself (zero length).
func eventEnd(vev *icomp, start time.Time, allDay bool) (time.Time, bool) {
	if l := vev.prop("DTEND"); l != nil {
		if t, _, ok := parseICalTime(l); ok {
			return t, true
		}
	}
	if d, ok := parseICalDuration(vev.propText("DURATION")); ok {
		return start.Add(d), true
	}
	if allDay {
		return start.Add(24 * time.Hour), true
	}
	return start, true
}

// busyStatus derives PidLidBusyStatus from TRANSP (transparent ⇒ free) and STATUS
// (tentative ⇒ tentative), defaulting to busy.
func busyStatus(vev *icomp) int32 {
	if strings.EqualFold(strings.TrimSpace(vev.propText("TRANSP")), "TRANSPARENT") {
		return busyFree
	}
	if strings.EqualFold(strings.TrimSpace(vev.propText("STATUS")), "TENTATIVE") {
		return busyTentative
	}
	return busyBusy
}

// classSensitivity maps iCalendar CLASS to PR_SENSITIVITY (PUBLIC ⇒ none).
func classSensitivity(c string) int32 {
	switch strings.ToUpper(strings.TrimSpace(c)) {
	case "PRIVATE":
		return mapi.SensitivityPrivate
	case "CONFIDENTIAL":
		return mapi.SensitivityConfidential
	}
	return mapi.SensitivityNone
}

// priorityImportance maps an iCalendar PRIORITY (1-9) to PR_IMPORTANCE; 1-4 high,
// 5 normal, 6-9 low. ok is false for an absent or 0 (undefined) priority.
func priorityImportance(s string) (int32, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	switch {
	case n >= 1 && n <= 4:
		return mapi.ImportanceHigh, true
	case n == 5:
		return mapi.ImportanceNormal, true
	case n >= 6 && n <= 9:
		return mapi.ImportanceLow, true
	}
	return 0, false
}

// alarmMinutes returns the reminder lead time in minutes from a VALARM TRIGGER
// duration (a "-PT15M" trigger is 15 minutes before the start).
func alarmMinutes(al *icomp) (int32, bool) {
	l := al.prop("TRIGGER")
	if l == nil {
		return 0, false
	}
	d, ok := parseICalDuration(l.value)
	if !ok {
		return 0, false
	}
	mins := int32(-d / time.Minute)
	if mins < 0 {
		mins = -mins
	}
	return mins, true
}

// generatedUID derives a deterministic UID for an event that carries none, so the
// same input yields the same identity. It is based on the summary.
func generatedUID(vev *icomp) string {
	base := vev.propText("SUMMARY")
	if base == "" {
		base = "event"
	}
	return "hermex-" + strings.Map(func(r rune) rune {
		if r == ' ' {
			return '-'
		}
		return r
	}, strings.ToLower(strings.TrimSpace(base)))
}

// setIf sets a string property only when the value is non-empty.
func setIf(p *mapi.PropertyValues, tag mapi.PropTag, v string) {
	if v != "" {
		p.Set(tag, v)
	}
}

// setNamedStr sets a named string property when its tag resolved and v is non-empty.
func setNamedStr(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName, v string) {
	if v == "" {
		return
	}
	if tag, ok := named[name]; ok {
		p.Set(tag, v)
	}
}

// setNamedTime sets a named PtSysTime property as a UTC FILETIME.
func setNamedTime(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName, t time.Time) {
	if tag, ok := named[name]; ok {
		p.Set(tag, mapi.UnixToNTTime(t.UTC()))
	}
}

// setNamedBool sets a named PtBoolean property.
func setNamedBool(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName, b bool) {
	if tag, ok := named[name]; ok {
		p.Set(tag, b)
	}
}

// setNamedLong sets a named PtLong property.
func setNamedLong(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName, n int32) {
	if tag, ok := named[name]; ok {
		p.Set(tag, n)
	}
}
