package oxcical

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
	"hermex/internal/recurrence"
)

// appointmentBlob renders a series master as the PidLidAppointmentRecur blob
// Outlook reads ([MS-OXOCAL] 2.2.1.44.5): the pattern its RRULE names, anchored at
// start in the series zone, every occurrence an EXDATE removes as a deleted
// instance, and every RECURRENCE-ID override as a modified instance with the
// changes it makes. An override cancelled with STATUS:CANCELLED removes its
// occurrence, so it is a deleted instance too. Without this a MAPI client shows
// the removed occurrences and the moved ones at their old times.
func appointmentBlob(cal, master *icomp, rrule string, start time.Time) ([]byte, error) {
	// A master whose span cannot be read repeats with no length, as before.
	s, _ := seriesShape(master)
	p, err := recurrence.AppointmentFromRRule(rrule, start, s.dur)
	if err != nil {
		return nil, err
	}
	loc := start.Location()
	for _, t := range exdates(master) {
		p.DeletedDates = append(p.DeletedDates, dayOf(t, loc))
	}
	_, overrides := splitSeries(cal)
	for _, key := range sortedKeys(overrides) {
		addOverride(&p, master, overrides[key], loc)
	}
	return recurrence.EncodeAppointment(p), nil
}

// addOverride records one RECURRENCE-ID override on the pattern.
func addOverride(p *recurrence.AppointmentPattern, master, ov *icomp, loc *time.Location) {
	rid, _, ok := parseICalTime(ov.prop("RECURRENCE-ID"))
	if !ok {
		return
	}
	p.DeletedDates = append(p.DeletedDates, dayOf(rid, loc))
	if strings.EqualFold(strings.TrimSpace(ov.propText("STATUS")), "CANCELLED") {
		return
	}
	start, allDay, ok := parseICalTime(ov.prop("DTSTART"))
	if !ok {
		return
	}
	end, _ := eventEnd(ov, start, allDay)
	ex := recurrence.Exception{
		Start:         recurrence.Minutes(start.In(loc)),
		End:           recurrence.Minutes(end.In(loc)),
		OriginalStart: recurrence.Minutes(rid.In(loc)),
	}
	overrideText(&ex, master, ov)
	if busy := busyStatus(ov); busy != busyStatus(master) {
		ex.Flags |= recurrence.OverrideBusyStatus
		ex.BusyStatus = busy
	}
	p.ModifiedDates = append(p.ModifiedDates, recurrence.Day(ex.Start))
	p.Exceptions = append(p.Exceptions, ex)
}

// overrideText records the subject and location an override changes.
func overrideText(ex *recurrence.Exception, master, ov *icomp) {
	if subject := ov.propText("SUMMARY"); ov.prop("SUMMARY") != nil && subject != master.propText("SUMMARY") {
		ex.Flags |= recurrence.OverrideSubject
		ex.Subject = subject
	}
	if location := ov.propText("LOCATION"); ov.prop("LOCATION") != nil && location != master.propText("LOCATION") {
		ex.Flags |= recurrence.OverrideLocation
		ex.Location = location
	}
}

// dayOf is the midnight of the day t falls on in the series zone, in pattern
// minutes.
func dayOf(t time.Time, loc *time.Location) uint32 {
	return recurrence.Day(recurrence.Minutes(t.In(loc)))
}

// sortedKeys returns the override keys in order, so the blob does not depend on
// map iteration.
func sortedKeys(m map[string]*icomp) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// EditRecurrenceBlob edits the PidLidAppointmentRecur blob of a stored series that
// keeps no iCalendar, the form a MAPI client writes: it removes the occurrence the
// series generates at at, or, when to is set, moves it to that span. The instance
// becomes a deleted day, and a moved one a modified instance too, so an earlier
// change to the same occurrence is replaced ([MS-OXOCAL] 2.2.1.44.5). ok is false
// when the object carries no pattern this package decodes.
func EditRecurrenceBlob(msg *oxcmail.Message, opt Options, at time.Time, to *Span) ([]byte, bool, error) {
	named, err := resolveFields(opt, exportFields, false)
	if err != nil {
		return nil, false, err
	}
	uidTag, err := resolveOne(opt, nameICalUID, mapi.PtUnicode, false)
	if err != nil {
		return nil, false, err
	}
	e := newEventExport(msg, named, uidTag, getStr(&msg.Props, mapi.PrMessageClass))
	if e.series == nil {
		return nil, false, nil
	}
	loc := e.wallZone()
	p := *e.series
	original := recurrence.Minutes(at.In(loc))
	dropException(&p, original)
	if day := recurrence.Day(original); !slices.Contains(p.DeletedDates, day) {
		p.DeletedDates = append(p.DeletedDates, day)
		slices.Sort(p.DeletedDates)
	}
	if to != nil {
		ex := recurrence.Exception{Start: recurrence.Minutes(to.Start.In(loc)), End: recurrence.Minutes(to.End.In(loc)), OriginalStart: original}
		p.Exceptions = append(p.Exceptions, ex)
		slices.SortFunc(p.Exceptions, func(a, b recurrence.Exception) int { return cmp.Compare(a.Start, b.Start) })
		p.ModifiedDates = append(p.ModifiedDates, recurrence.Day(ex.Start))
		slices.Sort(p.ModifiedDates)
	}
	return recurrence.EncodeAppointment(p), true, nil
}

// dropException removes the modified instance that replaces the occurrence
// generated at original, with its modified day.
func dropException(p *recurrence.AppointmentPattern, original uint32) {
	i := slices.IndexFunc(p.Exceptions, func(ex recurrence.Exception) bool { return ex.OriginalStart == original })
	if i < 0 {
		return
	}
	if j := slices.Index(p.ModifiedDates, recurrence.Day(p.Exceptions[i].Start)); j >= 0 {
		p.ModifiedDates = slices.Delete(p.ModifiedDates, j, j+1)
	}
	p.Exceptions = slices.Delete(p.Exceptions, i, i+1)
}

// RecurrenceBlob renders the PidLidAppointmentRecur blob of a stored series from
// its iCalendar, the same blob Import writes. A path that changes a stored
// series' iCalendar in place (an occurrence cancelled, moved or folded in) writes
// it again with the new body, or a MAPI client keeps reading the old occurrences.
// ok is false when the object is not a series master this package can encode.
func RecurrenceBlob(ical []byte) ([]byte, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return nil, false
	}
	master, _ := splitSeries(cal)
	if master == nil {
		return nil, false
	}
	rrule := master.prop("RRULE")
	start, allDay, ok := parseICalTime(master.prop("DTSTART"))
	if rrule == nil || !ok {
		return nil, false
	}
	if loc := eventZone(master); loc != nil && !allDay {
		start = start.In(loc)
	}
	blob, err := appointmentBlob(cal, master, rrule.value, start)
	return blob, err == nil
}
