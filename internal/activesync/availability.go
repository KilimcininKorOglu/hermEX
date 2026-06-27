package activesync

import (
	"strconv"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/ews"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// freeBusyPerms mirrors the EWS GetUserAvailability gate: a non-owner sees a
// target's busy blocks only with a free/busy or read-any right on that calendar.
// Without it the data is denied (an Availability status saying so), never shown
// as an all-free string.
const freeBusyPerms = mapi.FrightsFreeBusySimple | mapi.FrightsFreeBusyDetailed | mapi.FrightsReadAny

// mergedFreeBusySlot is the fixed interval each MergedFreeBusy digit covers
// (MS-ASCMD 2.2.3.107: every digit is one 30-minute slot).
const mergedFreeBusySlot = 30 * time.Minute

// Availability child Status values (MS-ASCMD 2.2.3.177.12).
const (
	availStatusSuccess = 1   // free/busy data was retrieved
	availStatusNoData  = 163 // could not be retrieved (no readable mailbox or no permission)
)

// availabilityWindow is the free/busy span an Availability request asks for. ok
// is false when Availability was not requested or the window is malformed.
type availabilityWindow struct {
	start, end time.Time
	ok         bool
}

// parseAvailability reads an Options>Availability request: the StartTime and
// EndTime bounding the free/busy window. A missing Availability element, an
// unparseable time (parseEASTime yields 0), or an end at or before the start
// yields a not-ok window, in which case the response simply omits Availability.
func parseAvailability(options *wbxml.Node) availabilityWindow {
	avail := options.Child(wbxml.RRAvailability)
	if avail == nil {
		return availabilityWindow{}
	}
	startSec := parseEASTime(avail.ChildText(wbxml.RRStartTime))
	endSec := parseEASTime(avail.ChildText(wbxml.RREndTime))
	if startSec == 0 || endSec == 0 || endSec <= startSec {
		return availabilityWindow{}
	}
	return availabilityWindow{
		start: time.Unix(startSec, 0).UTC(),
		end:   time.Unix(endSec, 0).UTC(),
		ok:    true,
	}
}

// availabilityNode builds a recipient's Availability response: a Status and, on
// success, the MergedFreeBusy string for the window.
func availabilityNode(e directory.GALEntry, win availabilityWindow, sess *session) *wbxml.Node {
	mfb, ok := mergedFreeBusy(e, win, sess)
	if !ok {
		return wbxml.Elem(wbxml.RRAvailability, wbxml.Str(wbxml.RRStatus, strconv.Itoa(availStatusNoData)))
	}
	return wbxml.Elem(wbxml.RRAvailability,
		wbxml.Str(wbxml.RRStatus, strconv.Itoa(availStatusSuccess)),
		wbxml.Str(wbxml.RRMergedFreeBusy, mfb))
}

// mergedFreeBusy computes the recipient's MergedFreeBusy string over the window,
// applying the free/busy permission gate. ok is false when the recipient has no
// local mailbox, the calendar cannot be read, or the caller is not entitled to
// the target's free/busy: in every such case the data is withheld (the caller
// learns nothing about the target's calendar), never returned as all-free (OWASP
// A01).
func mergedFreeBusy(e directory.GALEntry, win availabilityWindow, sess *session) (string, bool) {
	if e.StorePath == "" {
		return "", false // an external contact has no local calendar to read
	}
	st, err := objectstore.Open(e.StorePath)
	if err != nil {
		return "", false
	}
	defer st.Close()

	owner := strings.EqualFold(e.Address, sess.user) || e.StorePath == sess.mailbox
	if !owner {
		perm, err := st.ResolvePermission(int64(mapi.PrivateFIDCalendar), sess.user)
		if err != nil || perm&freeBusyPerms == 0 {
			return "", false
		}
	}

	// MergedFreeBusy carries only the per-slot status, no subject or location, so
	// the detailed view is never needed.
	events, err := ews.CalendarFreeBusy(st, win.start, win.end, false)
	if err != nil {
		return "", false
	}
	return quantizeFreeBusy(win.start, win.end, events), true
}

// quantizeFreeBusy renders busy blocks as a MergedFreeBusy string: one digit per
// 30-minute slot from start to end (the slot count rounded up), each digit the
// highest-priority status of any event overlapping that slot — Free 0 < Tentative
// 1 < Busy 2 < OOF 3. Per MS-ASCMD 2.2.3.107 the higher digit wins within a slot,
// so an event covering only part of a slot still claims the whole slot.
func quantizeFreeBusy(start, end time.Time, events []ews.CalendarEvent) string {
	total := end.Sub(start)
	if total <= 0 {
		return ""
	}
	slots := int(total / mergedFreeBusySlot)
	if total%mergedFreeBusySlot != 0 {
		slots++
	}
	digits := make([]byte, slots)
	for i := range digits {
		digits[i] = '0'
	}
	for _, ev := range events {
		d := busyDigit(ev.BusyType)
		if d == '0' {
			continue
		}
		evStart, err1 := time.Parse(time.RFC3339, ev.StartTime)
		evEnd, err2 := time.Parse(time.RFC3339, ev.EndTime)
		if err1 != nil || err2 != nil {
			continue
		}
		for i := 0; i < slots; i++ {
			slotStart := start.Add(time.Duration(i) * mergedFreeBusySlot)
			slotEnd := slotStart.Add(mergedFreeBusySlot)
			if evStart.Before(slotEnd) && evEnd.After(slotStart) && d > digits[i] {
				digits[i] = d
			}
		}
	}
	return string(digits)
}

// busyDigit maps a CalendarFreeBusy BusyType to its MergedFreeBusy digit.
func busyDigit(busyType string) byte {
	switch busyType {
	case "Tentative":
		return '1'
	case "Busy":
		return '2'
	case "OOF":
		return '3'
	default:
		return '0'
	}
}
