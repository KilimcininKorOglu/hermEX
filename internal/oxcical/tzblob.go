package oxcical

import (
	"encoding/binary"
	"time"
	"unicode/utf16"

	"hermex/internal/mapi"
)

// The TZRule flags of [MS-OXOCAL] 2.2.1.41.1: the rule a recurring series
// converts its times by, and the rule in effect.
const (
	tzRuleRecurCurrent uint16 = 0x0001
	tzRuleEffective    uint16 = 0x0002
)

// tzDefinitionValidKeyName is the TZDEFINITION header flag saying a KeyName
// follows.
const tzDefinitionValidKeyName uint16 = 0x0002

// tzReg is the time zone rule both blobs carry: the standard offset as a bias
// (minutes to add to local time to reach UTC), the daylight shift on top of it,
// and the relative dates the two switches fall on.
type tzReg struct {
	bias, standardBias, daylightBias int32
	standardDate, daylightDate       systemTime
}

// systemTime is the 16-byte SYSTEMTIME of [MS-DTYP]. In a zone rule a zero Year
// makes it a yearly date: Day is the week of the month (1 to 5, 5 being the last)
// the DayOfWeek falls in. A rule date has no year, second or millisecond, so
// those are blank fields, which binary.Write emits as zero.
type systemTime struct {
	_                                   uint16 // wYear
	month, dayOfWeek, day, hour, minute uint16
	_, _                                uint16 // wSecond, wMilliseconds
}

// zoneReg derives loc's rule as it stands in the year of at. A zone that switches
// twice that year gets both dates; any other zone is described by the offset in
// force at at alone, which is what a zone without daylight saving is.
func zoneReg(loc *time.Location, at time.Time) tzReg {
	switches := zoneSwitches(loc, at.In(loc).Year())
	if len(switches) != 2 {
		return tzReg{bias: minutesBias(offsetOf(at.In(loc)))}
	}
	std, dst := switches[0], switches[1]
	if std.dst {
		std, dst = dst, std
	}
	return tzReg{
		bias:         minutesBias(std.to),
		daylightBias: minutesBias(dst.to - std.to),
		standardDate: ruleDate(std.wall),
		daylightDate: ruleDate(dst.wall),
	}
}

// minutesBias turns seconds east of UTC into a Windows bias: minutes west of UTC.
func minutesBias(sec int) int32 {
	// #nosec G115 -- a UTC offset is under a day, far inside int32
	return int32(-sec / 60)
}

// ruleDate renders a switch's wall clock as the yearly SYSTEMTIME a zone rule
// holds: its month, weekday, week of the month and time of day.
func ruleDate(wall time.Time) systemTime {
	week := (wall.Day()-1)/7 + 1
	if wall.Day()+7 > daysIn(wall.Year(), wall.Month()) {
		week = 5
	}
	// #nosec G115 -- every field is a calendar component, small by construction
	return systemTime{
		month:     uint16(wall.Month()),
		dayOfWeek: uint16(wall.Weekday()),
		day:       uint16(week),
		hour:      uint16(wall.Hour()),
		minute:    uint16(wall.Minute()),
	}
}

// tzStructBlob renders PidLidTimeZoneStruct ([MS-OXOCAL] 2.2.1.39): the 48-byte
// TZREG, whose two year fields are zero because its dates are yearly ones.
func tzStructBlob(r tzReg) []byte {
	b := make([]byte, 0, 48)
	b = appendInt32LE(b, r.bias, r.standardBias, r.daylightBias)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = appendSystemTime(b, r.standardDate)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = appendSystemTime(b, r.daylightDate)
	return b
}

// tzDefinitionBlob renders a TZDEFINITION ([MS-OXOCAL] 2.2.1.41): a header naming
// the zone by keyName, then one TZRule in effect since year 1, the first year
// Exchange writes, with the given flags.
func tzDefinitionBlob(keyName string, r tzReg, flags uint16) []byte {
	name := utf16.Encode([]rune(keyName))
	b := make([]byte, 0, 8+2*len(name)+2+64)
	b = append(b, 0x02, 0x01)
	// #nosec G115 -- a zone name is a few dozen characters
	b = binary.LittleEndian.AppendUint16(b, uint16(6+2*len(name)))
	b = binary.LittleEndian.AppendUint16(b, tzDefinitionValidKeyName)
	// #nosec G115 -- a zone name is a few dozen characters
	b = binary.LittleEndian.AppendUint16(b, uint16(len(name)))
	for _, u := range name {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = append(b, 0x02, 0x01)
	b = binary.LittleEndian.AppendUint16(b, 0x003E)
	b = binary.LittleEndian.AppendUint16(b, flags)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = append(b, make([]byte, 14)...)
	b = appendInt32LE(b, r.bias, r.standardBias, r.daylightBias)
	b = appendSystemTime(b, r.standardDate)
	b = appendSystemTime(b, r.daylightDate)
	return b
}

// DisplayZone returns the zone a stored appointment's start is shown in: the zone
// its PidLidAppointmentTimeZoneDefinitionStartDisplay names by KeyName, resolved
// as a Windows id or an IANA name. It returns nil when the property is absent or
// names a zone neither table knows.
func DisplayZone(props mapi.PropertyValues, opt Options) *time.Location {
	tag, err := resolveOne(opt, mapi.NameAppointmentTimeZoneDefStartDisplay, mapi.PtBinary, false)
	if err != nil || tag == 0 {
		return nil
	}
	v, ok := props.Get(tag)
	if !ok {
		return nil
	}
	blob, ok := v.([]byte)
	if !ok {
		return nil
	}
	return ZoneByID(tzDefinitionKeyName(blob))
}

// tzDefinitionKeyName reads the KeyName of a TZDEFINITION ([MS-OXOCAL]
// 2.2.1.41), or "" when the header carries none or is cut short.
func tzDefinitionKeyName(b []byte) string {
	if len(b) < 8 || binary.LittleEndian.Uint16(b[4:6])&tzDefinitionValidKeyName == 0 {
		return ""
	}
	n := int(binary.LittleEndian.Uint16(b[6:8]))
	if len(b) < 8+2*n {
		return ""
	}
	units := make([]uint16, n)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[8+2*i:])
	}
	return string(utf16.Decode(units))
}

// appendInt32LE appends each signed 32-bit integer in little-endian byte order.
func appendInt32LE(b []byte, values ...int32) []byte {
	for _, v := range values {
		// #nosec G115 -- bit-preserving two's complement encoding for little-endian wire format
		b = binary.LittleEndian.AppendUint32(b, uint32(v))
	}
	return b
}

// appendSystemTime appends a 16-byte SYSTEMTIME in little-endian byte order.
func appendSystemTime(b []byte, st systemTime) []byte {
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint16(b, st.month)
	b = binary.LittleEndian.AppendUint16(b, st.dayOfWeek)
	b = binary.LittleEndian.AppendUint16(b, st.day)
	b = binary.LittleEndian.AppendUint16(b, st.hour)
	b = binary.LittleEndian.AppendUint16(b, st.minute)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint16(b, 0)
	return b
}
