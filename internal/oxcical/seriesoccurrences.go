package oxcical

import (
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
	"hermex/internal/recurrence"
)

// Occurrence is one instance of a stored series with the details a free/busy
// answer reports for it: those its exception changes, else the series' own.
type Occurrence struct {
	Span
	// At is the instant the series generates for the occurrence, its RECURRENCE-ID;
	// an exception may have moved Span away from it.
	At          time.Time
	Subject     string
	Location    string
	BusyStatus  int32
	ReminderSet bool
	// Exception reports that an exception of the series changed the occurrence.
	Exception bool
}

// SeriesOccurrences expands a stored recurring appointment into the occurrences
// that overlap the range. Each carries the subject, location, busy status and
// reminder its ExceptionInfo overrides ([MS-OXOCAL] 2.2.1.44.2), else those of
// the series. ok is false when the object does not expand as a series.
func SeriesOccurrences(msg *oxcmail.Message, opt Options, rangeStart, rangeEnd time.Time) (occ []Occurrence, ok bool, err error) {
	ical, err := Export(msg, opt)
	if err != nil {
		return nil, false, err
	}
	insts, ok := InstancesIn(ical, rangeStart, rangeEnd)
	if !ok {
		return nil, false, nil
	}
	named, err := resolveFields(opt, exportFields, false)
	if err != nil {
		return nil, false, err
	}
	uidTag, err := resolveOne(opt, nameICalUID, mapi.PtUnicode, false)
	if err != nil {
		return nil, false, err
	}
	e := newEventExport(msg, named, uidTag, getStr(&msg.Props, mapi.PrMessageClass))
	base := e.seriesDetails()
	exceptions := e.exceptionsByOriginal()
	occ = make([]Occurrence, 0, len(insts))
	for _, inst := range insts {
		o := base
		o.Span = inst.Span
		o.At = inst.At
		if ex, found := exceptions[inst.At.Unix()]; found {
			applyException(&o, ex)
		}
		occ = append(occ, o)
	}
	return occ, true, nil
}

// seriesDetails reads the details every occurrence without an exception shares.
func (e *eventExport) seriesDetails() Occurrence {
	busy, _ := namedLong(e.p, e.named, mapi.NameBusyStatus)
	return Occurrence{
		Subject:     getStr(e.p, mapi.PrSubject),
		Location:    namedStr(e.p, e.named, mapi.NameAppointmentLocation),
		BusyStatus:  busy,
		ReminderSet: namedBool(e.p, e.named, mapi.NameReminderSet),
	}
}

// exceptionsByOriginal keys the series' exceptions by the instant of the
// occurrence each replaces, the RECURRENCE-ID the export gives its override.
func (e *eventExport) exceptionsByOriginal() map[int64]*recurrence.Exception {
	if e.series == nil {
		return nil
	}
	loc := e.wallZone()
	byStart := make(map[int64]*recurrence.Exception, len(e.series.Exceptions))
	for i := range e.series.Exceptions {
		ex := &e.series.Exceptions[i]
		byStart[recurrence.WallClock(ex.OriginalStart, loc).Unix()] = ex
	}
	return byStart
}

// applyException replaces each detail the exception's flags say it changes.
func applyException(o *Occurrence, ex *recurrence.Exception) {
	o.Exception = true
	o.Subject = overridden(ex, recurrence.OverrideSubject, ex.Subject, o.Subject)
	o.Location = overridden(ex, recurrence.OverrideLocation, ex.Location, o.Location)
	if ex.Flags&recurrence.OverrideBusyStatus != 0 {
		o.BusyStatus = ex.BusyStatus
	}
	if ex.Flags&recurrence.OverrideReminder != 0 {
		o.ReminderSet = ex.ReminderSet
	}
}
