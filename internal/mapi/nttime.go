package mapi

import "time"

// timeFixupSeconds is the number of seconds between the FILETIME epoch
// (1601-01-01 UTC) and the Unix epoch (1970-01-01 UTC).
const timeFixupSeconds = 11644473600

// UnixToNTTime converts a Go time to an NT FILETIME: 100-nanosecond ticks since
// 1601-01-01 UTC, the on-the-wire form of a PtSysTime value
// (rop_util_unix_to_nttime). Sub-100ns precision is dropped.
func UnixToNTTime(t time.Time) uint64 {
	return uint64(t.Unix()+timeFixupSeconds)*10000000 + uint64(t.Nanosecond()/100)
}

// NTTimeToUnix converts an NT FILETIME back to a Go time in UTC
// (rop_util_nttime_to_unix), at 100-nanosecond resolution.
func NTTimeToUnix(nt uint64) time.Time {
	sec := int64(nt/10000000) - timeFixupSeconds
	nsec := int64(nt%10000000) * 100
	return time.Unix(sec, nsec).UTC()
}
