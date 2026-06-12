package mapi

// SystemTime is the 16-byte MAPI SYSTEMTIME calendar structure: eight
// little-endian int16 fields. It is used inside time-zone definitions and is
// distinct from a PtSysTime property value, which is an 8-byte FILETIME.
type SystemTime struct {
	Year         int16
	Month        int16
	DayOfWeek    int16
	Day          int16
	Hour         int16
	Minute       int16
	Second       int16
	Milliseconds int16
}
