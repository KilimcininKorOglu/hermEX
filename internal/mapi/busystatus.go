package mapi

// PidLidBusyStatus values ([MS-OXOCAL] 2.2.1.2.3): how an appointment shows on
// the attendee's free/busy.
const (
	BusyFree             int32 = 0
	BusyTentative        int32 = 1
	BusyBusy             int32 = 2
	BusyOOF              int32 = 3
	BusyWorkingElsewhere int32 = 4
)

// BusyStatusOccupies reports whether an appointment with this status takes the
// attendee, so a scheduler must treat it as a conflict and a free/busy answer
// must report the time as taken.
//
// Working elsewhere does NOT occupy: it is the status Outlook writes for a home
// office day, and it says where the attendee is rather than that they are
// unavailable. Treating it as busy empties a whole day of candidate times for
// everyone who records one. Out of office does occupy, because the attendee is
// away rather than merely elsewhere.
//
// A value outside the five is read as free, which is how the reference clamps an
// absent or out-of-range status.
func BusyStatusOccupies(status int32) bool {
	switch status {
	case BusyTentative, BusyBusy, BusyOOF:
		return true
	}
	return false
}
