package recurrence

import (
	"encoding/binary"
	"errors"
	"time"
	"unicode/utf16"
)

// Override flags of an ExceptionInfo ([MS-OXOCAL] 2.2.1.44.2 OverrideFlags): which
// fields of a modified instance differ from the series and are therefore present.
const (
	OverrideSubject       uint16 = 0x0001
	OverrideMeetingType   uint16 = 0x0002
	OverrideReminderDelta uint16 = 0x0004
	OverrideReminder      uint16 = 0x0008
	OverrideLocation      uint16 = 0x0010
	OverrideBusyStatus    uint16 = 0x0020
	OverrideAttachment    uint16 = 0x0040
	OverrideSubType       uint16 = 0x0080
	OverrideColor         uint16 = 0x0100
)

// changeHighlightVersion is the WriterVersion2 from which an ExtendedException
// starts with a ChangeHighlight block ([MS-OXOCAL] 2.2.1.44.4).
const changeHighlightVersion uint32 = 0x00003009

// errTruncated reports a blob that ends inside a field it declares.
var errTruncated = errors.New("recurrence: truncated AppointmentRecurrencePattern")

// AppointmentPattern is a decoded PidLidAppointmentRecur blob: the recurrence, the
// original days of the deleted and modified instances, the time of day every
// occurrence spans, and the modified instances themselves ([MS-OXOCAL] 2.2.1.44.5).
// Every date and time is a wall clock in the series zone, counted in minutes since
// midnight, January 1, 1601.
//
// A blob that ends after the RecurrencePattern, the shape FromRRule writes, decodes
// with HasTimes false and no exceptions.
type AppointmentPattern struct {
	Pattern
	DeletedDates    []uint32 // midnight of the original day of every deleted or modified instance
	ModifiedDates   []uint32 // midnight of the day of every modified instance
	HasTimes        bool     // StartTimeOffset and EndTimeOffset were present
	StartTimeOffset uint32   // minutes after midnight each occurrence starts
	EndTimeOffset   uint32   // minutes after midnight each occurrence ends
	Exceptions      []Exception
}

// Exception is one modified instance: its ExceptionInfo ([MS-OXOCAL] 2.2.1.44.2)
// joined with the ExtendedException that carries its Unicode text (2.2.1.44.4).
// Flags says which of the optional fields are set.
type Exception struct {
	Start, End, OriginalStart uint32 // wall clock minutes since 1601
	Flags                     uint16
	Subject                   string
	Location                  string
	BusyStatus                int32
	ReminderDelta             int32
	ReminderSet               bool
	AllDay                    bool
}

// DecodeAppointment parses a PidLidAppointmentRecur blob. The exceptions are read
// only when their count matches the pattern's ModifiedInstanceCount, the check
// [MS-OXOCAL] places on ExceptionCount; a blob that breaks it keeps its pattern and
// dates and loses only the exception details.
func DecodeAppointment(b []byte) (AppointmentPattern, error) {
	r := &reader{b: b}
	p, err := decodePattern(r)
	if err != nil {
		return AppointmentPattern{}, err
	}
	if r.left() == 0 {
		return p, nil
	}
	if err := decodeTimes(r, &p); err != nil {
		return AppointmentPattern{}, err
	}
	return p, nil
}

// decodePattern reads the RecurrencePattern ([MS-OXOCAL] 2.2.1.44.1) the blob opens
// with, keeping its instance date lists.
func decodePattern(r *reader) (AppointmentPattern, error) {
	base, err := UnmarshalBinary(r.b)
	if err != nil {
		return AppointmentPattern{}, err
	}
	p := AppointmentPattern{Pattern: base}
	r.off = patternSpecificEnd(base.PatternType) + 12 // past EndType, OccurrenceCount and FirstDOW
	p.DeletedDates = r.dates()
	p.ModifiedDates = r.dates()
	r.skip(8) // StartDate and EndDate, already in Pattern
	return p, r.err
}

// patternSpecificEnd is the offset at which the PatternTypeSpecific field ends,
// which UnmarshalBinary has already validated for the pattern type.
func patternSpecificEnd(patternType uint16) int {
	if patternType == PatternMonthNth {
		return 22 + 8
	}
	return 22 + 4
}

// decodeTimes reads what an AppointmentRecurrencePattern adds to the pattern: the
// occurrence time of day and the modified instances.
func decodeTimes(r *reader, p *AppointmentPattern) error {
	r.skip(4) // ReaderVersion2
	writer := r.u32()
	p.StartTimeOffset = r.u32()
	p.EndTimeOffset = r.u32()
	count := int(r.u16())
	if r.err != nil {
		return r.err
	}
	p.HasTimes = true
	if count != len(p.ModifiedDates) {
		return nil
	}
	ex, err := decodeExceptions(r, count, writer)
	if err != nil {
		return err
	}
	p.Exceptions = ex
	return nil
}

// decodeExceptions reads count ExceptionInfo structures, the reserved block after
// them, and the ExtendedException structures that match them one for one.
func decodeExceptions(r *reader, count int, writer uint32) ([]Exception, error) {
	// Every ExceptionInfo is at least 14 bytes, so a count the rest of the blob
	// cannot hold is refused before anything is allocated for it.
	if count*14 > r.left() {
		return nil, errTruncated
	}
	ex := make([]Exception, count)
	for i := range ex {
		readExceptionInfo(r, &ex[i])
	}
	r.skip(int(r.u32())) // ReservedBlock1
	for i := range ex {
		readExtendedException(r, &ex[i], writer)
	}
	return ex, r.err
}

// readExceptionInfo reads one ExceptionInfo. Its 8-bit subject and location are kept
// only until the ExtendedException supplies the Unicode text.
func readExceptionInfo(r *reader, e *Exception) {
	e.Start, e.End, e.OriginalStart = r.u32(), r.u32(), r.u32()
	e.Flags = r.u16()
	if e.Flags&OverrideSubject != 0 {
		r.skip(2)
		e.Subject = latin1(r.bytes(int(r.u16())))
	}
	if e.Flags&OverrideMeetingType != 0 {
		r.skip(4)
	}
	if e.Flags&OverrideReminderDelta != 0 {
		e.ReminderDelta = int32(r.u32()) // #nosec G115 -- a signed MAPI long carried in four bytes
	}
	if e.Flags&OverrideReminder != 0 {
		e.ReminderSet = r.u32() != 0
	}
	if e.Flags&OverrideLocation != 0 {
		r.skip(2)
		e.Location = latin1(r.bytes(int(r.u16())))
	}
	readExceptionTail(r, e)
}

// readExceptionTail reads the fixed-size fields that close an ExceptionInfo.
func readExceptionTail(r *reader, e *Exception) {
	if e.Flags&OverrideBusyStatus != 0 {
		e.BusyStatus = int32(r.u32()) // #nosec G115 -- a signed MAPI long carried in four bytes
	}
	if e.Flags&OverrideAttachment != 0 {
		r.skip(4)
	}
	if e.Flags&OverrideSubType != 0 {
		e.AllDay = r.u32() != 0
	}
	if e.Flags&OverrideColor != 0 {
		r.skip(4)
	}
}

// readExtendedException reads one ExtendedException and replaces the 8-bit subject
// and location with its Unicode ones.
func readExtendedException(r *reader, e *Exception, writer uint32) {
	if writer >= changeHighlightVersion {
		r.skip(int(r.u32())) // ChangeHighlight
	}
	r.skip(int(r.u32())) // ReservedBlockEE1
	if e.Flags&(OverrideSubject|OverrideLocation) == 0 {
		return
	}
	r.skip(12) // StartDateTime, EndDateTime, OriginalStartDate repeat the ExceptionInfo
	if e.Flags&OverrideSubject != 0 {
		e.Subject = r.utf16(int(r.u16()))
	}
	if e.Flags&OverrideLocation != 0 {
		e.Location = r.utf16(int(r.u16()))
	}
	r.skip(int(r.u32())) // ReservedBlockEE2
}

// WallClock returns the wall clock a count of minutes since midnight, January 1,
// 1601 names, in loc. The pattern's dates and times are wall clocks in the series
// zone, so they are placed by their fields rather than by adding a duration.
func WallClock(minutes uint32, loc *time.Location) time.Time {
	t := timeFromMinutes(minutes)
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
}

// RRuleUntil renders the pattern as an RRULE whose end-by-date bound is until, the
// last occurrence's start, instead of the midnight EndDate records. An UNTIL at that
// midnight would drop the last occurrence of any series that starts later in the day.
func (p Pattern) RRuleUntil(until time.Time) (string, bool) {
	return p.rrule(until)
}

// latin1 decodes an 8-bit string byte for byte, the most a non-Unicode field says
// about its characters without the writer's code page.
func latin1(b []byte) string {
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}
	return string(runes)
}

// reader walks a little-endian blob. The first read past the end sets err and
// every later read returns zero, so a decoder checks err once at the end.
type reader struct {
	b   []byte
	off int
	err error
}

// left is the number of bytes not yet read.
func (r *reader) left() int {
	if r.err != nil || r.off > len(r.b) {
		return 0
	}
	return len(r.b) - r.off
}

// take returns the next n bytes, or nil once the blob is exhausted.
func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > r.left() {
		r.err = errTruncated
		return nil
	}
	b := r.b[r.off : r.off+n]
	r.off += n
	return b
}

func (r *reader) skip(n int) { r.take(n) }

func (r *reader) bytes(n int) []byte { return r.take(n) }

func (r *reader) u16() uint16 {
	if b := r.take(2); b != nil {
		return binary.LittleEndian.Uint16(b)
	}
	return 0
}

func (r *reader) u32() uint32 {
	if b := r.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

// utf16 reads n UTF-16LE code units.
func (r *reader) utf16(n int) string {
	b := r.take(2 * n)
	if b == nil {
		return ""
	}
	units := make([]uint16, n)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(units))
}

// dates reads a count followed by that many four-byte dates, refusing a count the
// rest of the blob cannot hold before allocating for it.
func (r *reader) dates() []uint32 {
	n := int(r.u32())
	if r.err != nil || n == 0 {
		return nil
	}
	if n > r.left()/4 {
		r.err = errTruncated
		return nil
	}
	out := make([]uint32, n)
	for i := range out {
		out[i] = r.u32()
	}
	return out
}
