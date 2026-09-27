package recurrence

import (
	"cmp"
	"encoding/binary"
	"slices"
	"time"
	"unicode/utf16"
)

// appointmentReaderVersion is the ReaderVersion2 every AppointmentRecurrencePattern
// carries ([MS-OXOCAL] 2.2.1.44.5).
const appointmentReaderVersion uint32 = 0x00003006

// AppointmentFromRRule builds the AppointmentRecurrencePattern of a series whose
// first occurrence starts at start and lasts dur, with no deleted or modified
// instance yet. start is read as a wall clock in its own location, the series
// zone: the pattern's StartDate is that day's midnight and every occurrence starts
// StartTimeOffset minutes after it ([MS-OXOCAL] 2.2.1.44.1 and 2.2.1.44.5).
func AppointmentFromRRule(rruleText string, start time.Time, dur time.Duration) (AppointmentPattern, error) {
	rec, err := parseRRule(rruleText)
	if err != nil {
		return AppointmentPattern{}, err
	}
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())
	p, err := patternFromRecurrence(rec, day)
	if err != nil {
		return AppointmentPattern{}, err
	}
	offset := Minutes(start) - Minutes(day)
	// #nosec G115 -- an occurrence lasts at most the span the series repeats in, far below the 32-bit minute range
	end := offset + uint32(max(dur, 0)/time.Minute)
	return AppointmentPattern{Pattern: p, HasTimes: true, StartTimeOffset: offset, EndTimeOffset: end}, nil
}

// Minutes returns the wall clock t names, in t's own location, as minutes since
// midnight, January 1, 1601: the unit every date and time of a pattern is in.
func Minutes(t time.Time) uint32 { return minutesSince1601(t) }

// Day returns the midnight of the day minutes falls on.
func Day(minutes uint32) uint32 { return minutes - minutes%(24*60) }

// EncodeAppointment renders p as a PidLidAppointmentRecur blob: the
// RecurrencePattern with its deleted and modified instance dates, the occurrence
// time of day, and one ExceptionInfo plus one ExtendedException per modified
// instance ([MS-OXOCAL] 2.2.1.44.5). The instance dates are written in ascending
// order and the exceptions in the order of the dates they moved to, as the
// structure requires.
func EncodeAppointment(p AppointmentPattern) []byte {
	deleted := sortedDates(p.DeletedDates)
	modified := sortedDates(p.ModifiedDates)
	ex := slices.Clone(p.Exceptions)
	slices.SortStableFunc(ex, func(a, b Exception) int { return cmp.Compare(a.Start, b.Start) })

	w := &writer{b: p.marshal(deleted, modified)}
	w.u32(appointmentReaderVersion)
	w.u32(changeHighlightVersion) // WriterVersion2
	w.u32(p.StartTimeOffset)
	w.u32(p.EndTimeOffset)
	// #nosec G115 -- one exception per modified instance, which a series holds far fewer than 65536 of
	w.u16(uint16(len(ex)))
	for i := range ex {
		w.exceptionInfo(&ex[i])
	}
	w.u32(0) // ReservedBlock1Size
	for i := range ex {
		w.extendedException(&ex[i])
	}
	w.u32(0) // ReservedBlock2Size
	return w.b
}

// sortedDates returns the distinct dates in ascending order.
func sortedDates(dates []uint32) []uint32 {
	out := slices.Clone(dates)
	slices.Sort(out)
	return slices.Compact(out)
}

// writer appends little-endian fields to a blob.
type writer struct{ b []byte }

func (w *writer) u16(v uint16) { w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *writer) u32(v uint32) { w.b = binary.LittleEndian.AppendUint32(w.b, v) }

// bool32 writes a flag as the four-byte value an ExceptionInfo carries.
func (w *writer) bool32(v bool) {
	if v {
		w.u32(1)
		return
	}
	w.u32(0)
}

// text8 writes the 8-bit form of a subject or location: its length with the
// terminator, its length without, and the characters. A character outside Latin-1
// has no 8-bit form without a code page and is written as '?'; the
// ExtendedException carries the exact Unicode text.
func (w *writer) text8(s string) {
	var b []byte
	for _, r := range s {
		if r > 0xFF {
			r = '?'
		}
		b = append(b, byte(r))
	}
	// #nosec G115 -- a subject or location is bounded well below 65535 characters by every client that sets one
	n := uint16(len(b))
	w.u16(n + 1)
	w.u16(n)
	w.b = append(w.b, b...)
}

// text16 writes a Unicode subject or location: its length in UTF-16 code units and
// the code units.
func (w *writer) text16(s string) {
	units := utf16.Encode([]rune(s))
	// #nosec G115 -- a subject or location is bounded well below 65535 code units by every client that sets one
	w.u16(uint16(len(units)))
	for _, u := range units {
		w.u16(u)
	}
}

// exceptionInfo writes one ExceptionInfo ([MS-OXOCAL] 2.2.1.44.2). Only the fields
// its flags name follow the fixed part, in the order the structure gives them.
func (w *writer) exceptionInfo(e *Exception) {
	w.u32(e.Start)
	w.u32(e.End)
	w.u32(e.OriginalStart)
	w.u16(e.Flags)
	if e.Flags&OverrideSubject != 0 {
		w.text8(e.Subject)
	}
	// MeetingType, Attachment and AppointmentColor are not modelled: a flag that
	// names one still gets its four bytes, so the structure stays readable.
	if e.Flags&OverrideMeetingType != 0 {
		w.u32(0)
	}
	if e.Flags&OverrideReminderDelta != 0 {
		w.u32(uint32(e.ReminderDelta)) // #nosec G115 -- a signed MAPI long carried in four bytes
	}
	if e.Flags&OverrideReminder != 0 {
		w.bool32(e.ReminderSet)
	}
	if e.Flags&OverrideLocation != 0 {
		w.text8(e.Location)
	}
	if e.Flags&OverrideBusyStatus != 0 {
		w.u32(uint32(e.BusyStatus)) // #nosec G115 -- a signed MAPI long carried in four bytes
	}
	if e.Flags&OverrideAttachment != 0 {
		w.u32(0)
	}
	if e.Flags&OverrideSubType != 0 {
		w.bool32(e.AllDay)
	}
	if e.Flags&OverrideColor != 0 {
		w.u32(0)
	}
}

// extendedException writes one ExtendedException ([MS-OXOCAL] 2.2.1.44.4): an
// empty ChangeHighlight, and the Unicode subject and location when the exception
// overrides either.
func (w *writer) extendedException(e *Exception) {
	w.u32(4) // ChangeHighlightSize
	w.u32(0) // ChangeHighlightValue
	w.u32(0) // ReservedBlockEE1Size
	if e.Flags&(OverrideSubject|OverrideLocation) == 0 {
		return
	}
	w.u32(e.Start)
	w.u32(e.End)
	w.u32(e.OriginalStart)
	if e.Flags&OverrideSubject != 0 {
		w.text16(e.Subject)
	}
	if e.Flags&OverrideLocation != 0 {
		w.text16(e.Location)
	}
	w.u32(0) // ReservedBlockEE2Size
}
