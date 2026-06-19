package activesync

import (
	"encoding/base64"
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// utcTimezone is the MS-ASCAL Timezone field for an appointment whose times are
// stored in UTC: a base64 TIME_ZONE_INFORMATION (172 bytes) with a zero bias and
// no daylight rule, so the UTC StartTime/EndTime carry no further adjustment.
var utcTimezone = base64.StdEncoding.EncodeToString(make([]byte, 172))

// easCalTime formats a UTC instant as MS-ASCAL's compact appointment time,
// YYYYMMDDThhmmssZ.
func easCalTime(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// calendarAppData builds the ActiveSync ApplicationData for one stored appointment
// (MS-ASCAL): its subject, start/end (UTC), location, all-day flag, busy status,
// and modification time stamp, read from the calendar named properties. Times ride
// in a UTC timezone, so the stored UTC instants need no conversion. Recurrence and
// attendees are later increments. It returns nil when the object lacks the
// start/end that make it an appointment (the calendar folder may hold none).
func calendarAppData(st *objectstore.Store, objectID int64) (*wbxml.Node, error) {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole, // 0
		mapi.NameAppointmentEndWhole,   // 1
		mapi.NameBusyStatus,            // 2
		mapi.NameAppointmentLocation,   // 3
		mapi.NameAppointmentSubType,    // 4
	})
	if err != nil {
		return nil, err
	}
	if ids[0] == 0 || ids[1] == 0 {
		return nil, nil // the mailbox has never stored an appointment
	}
	startTag := mapi.MakeTag(ids[0], mapi.PtSysTime)
	endTag := mapi.MakeTag(ids[1], mapi.PtSysTime)
	busyTag := mapi.MakeTag(ids[2], mapi.PtLong)
	locTag := mapi.MakeTag(ids[3], mapi.PtUnicode)
	allDayTag := mapi.MakeTag(ids[4], mapi.PtBoolean)

	pv, err := st.GetMessageProperties(objectID, startTag, endTag, busyTag, locTag, allDayTag,
		mapi.PrSubject, mapi.PrLastModificationTime)
	if err != nil {
		return nil, err
	}
	start, ok1 := ntTimeProp(pv, startTag)
	end, ok2 := ntTimeProp(pv, endTag)
	if !ok1 || !ok2 {
		return nil, nil
	}
	stamp := start
	if mod, ok := ntTimeProp(pv, mapi.PrLastModificationTime); ok {
		stamp = mod
	}

	data := wbxml.Elem(wbxml.ASData,
		wbxml.Str(wbxml.CalTimezone, utcTimezone),
		wbxml.Str(wbxml.CalDtStamp, easCalTime(stamp)),
		wbxml.Str(wbxml.CalStartTime, easCalTime(start)),
		wbxml.Str(wbxml.CalSubject, stringProp(pv, mapi.PrSubject)),
		wbxml.Str(wbxml.CalEndTime, easCalTime(end)),
		wbxml.Str(wbxml.CalBusyStatus, strconv.Itoa(int(longProp(pv, busyTag)))),
		wbxml.Str(wbxml.CalAllDayEvent, boolStr(boolProp(pv, allDayTag))),
		// No attendees are emitted yet, so the appointment is not a meeting.
		wbxml.Str(wbxml.CalMeetingStatus, "0"),
	)
	if loc := stringProp(pv, locTag); loc != "" {
		data.Children = append(data.Children, wbxml.Str(wbxml.CalLocation, loc))
	}
	return data, nil
}

// ntTimeProp reads a PtSysTime property (stored as an NT-time uint64) as a UTC
// instant; tag 0 or an absent/!uint64 value reports not-present.
func ntTimeProp(pv mapi.PropertyValues, tag mapi.PropTag) (time.Time, bool) {
	if tag == 0 {
		return time.Time{}, false
	}
	if v, ok := pv.Get(tag); ok {
		if nt, ok := v.(uint64); ok {
			return mapi.NTTimeToUnix(nt).UTC(), true
		}
	}
	return time.Time{}, false
}

func longProp(pv mapi.PropertyValues, tag mapi.PropTag) int32 {
	if v, ok := pv.Get(tag); ok {
		if n, ok := v.(int32); ok {
			return n
		}
	}
	return 0
}

func boolProp(pv mapi.PropertyValues, tag mapi.PropTag) bool {
	if v, ok := pv.Get(tag); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func stringProp(pv mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := pv.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
