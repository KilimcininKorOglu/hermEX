package meeting

import (
	"errors"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/relay"
)

// AutoProcess applies a mailbox's automatic meeting-request processing to a
// just-delivered message at messageID. It reports handled=true when the message was a
// meeting request the mailbox auto-processes, so the delivery path skips the
// out-of-office reply (a meeting request is answered with a meeting response, not an
// OOF).
//
// AutoAccept is the master switch: a mailbox with it off auto-processes nothing and
// the request is left in the inbox for manual handling. With it on, the request is
// declined when it is recurring and DeclineRecurring is set, or when it conflicts with
// an existing appointment and DeclineConflict is set; a conflict-free request is
// accepted; any remaining (conflicting but not declined) request is filed tentatively.
// An accept or decline notifies the organizer (an iTIP REPLY); a tentative filing does
// not. The booking and notification are delegated to Respond, which also decides
// what happens to the just-delivered inbox copy: it stays where it is unless the
// mailbox has RemoveRequestOnResponse set, in which case it goes to Deleted Items
// like any other answered request.
//
// spool routes the organizer notification; passing nil keeps it local-only (an
// external organizer is not notified), matching the out-of-office reply's behavior.
func AutoProcess(st *objectstore.Store, accounts directory.Accounts, spool *relay.Spool, recipient string, messageID int64) (bool, error) {
	cfg, err := st.GetMeetingConfig()
	if err != nil {
		return false, err
	}
	if !cfg.AutoAccept {
		return false, nil // master off: no automatic processing
	}
	req, err := openMeetingMessage(st, messageID)
	if errors.Is(err, ErrRequestNotFound) {
		return false, nil // a message that is gone is not auto-processed
	}
	if err != nil {
		return false, err
	}
	if propStr(req.Props, mapi.PrMessageClass) != requestClass {
		return false, nil
	}
	resp, err := decideResponse(st, req, cfg)
	if err != nil {
		return true, err
	}
	// A tentative outcome books the request silently; an accept or decline tells the
	// organizer.
	_, err = respondAutomatically(st, accounts, spool, recipient, messageID, resp, resp != ResponseTentative)
	return true, err
}

// apptTags are the appointment named-property tags the auto-processor reads.
type apptTags struct {
	start, end, busy, recur, uid mapi.PropTag
}

// resolveApptTags resolves the appointment start/end/busy/recurring/UID named tags.
func resolveApptTags(st *objectstore.Store) (apptTags, error) {
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole,
		mapi.NameAppointmentEndWhole,
		mapi.NameBusyStatus,
		mapi.NameRecurring,
		mapi.NameICalUID,
	})
	if err != nil {
		return apptTags{}, err
	}
	return apptTags{
		start: mapi.MakeTag(ids[0], mapi.PtSysTime),
		end:   mapi.MakeTag(ids[1], mapi.PtSysTime),
		busy:  mapi.MakeTag(ids[2], mapi.PtLong),
		recur: mapi.MakeTag(ids[3], mapi.PtBoolean),
		uid:   mapi.MakeTag(ids[4], mapi.PtUnicode),
	}, nil
}

// decideResponse applies the configured rules to a meeting request.
func decideResponse(st *objectstore.Store, req *oxcmail.Message, cfg objectstore.MeetingConfig) (int32, error) {
	tags, err := resolveApptTags(st)
	if err != nil {
		return 0, err
	}
	if cfg.DeclineRecurring && boolVal(req.Props, tags.recur) {
		return ResponseDeclined, nil
	}
	conflict, err := hasConflict(st, req, tags)
	if err != nil {
		return 0, err
	}
	if cfg.DeclineConflict && conflict {
		return ResponseDeclined, nil
	}
	if !conflict {
		return ResponseAccepted, nil
	}
	return ResponseTentative, nil
}

// hasConflict reports whether the request's time window overlaps an existing busy
// appointment in the Calendar. A recurring appointment is expanded into the
// instances that fall in the window, so a series occupies the days it actually
// occupies. Only a Busy or out-of-office appointment is a hard conflict; free and
// tentative are not.
func hasConflict(st *objectstore.Store, req *oxcmail.Message, t apptTags) (bool, error) {
	reqStart, ok1 := ntTime(req.Props, t.start)
	reqEnd, ok2 := ntTime(req.Props, t.end)
	if !ok1 || !ok2 {
		return false, nil // a request with no time window cannot be judged in conflict
	}
	reqUID := propStr(req.Props, t.uid)
	// The store applies the request's own window, so the scan costs the number of
	// appointments that could overlap rather than the size of the calendar. This
	// runs on the delivery path, once per inbound meeting request. The store's
	// predicate is wider than the overlap test below, which is unchanged.
	objs, err := st.ListFolderObjectsInWindow(int64(mapi.PrivateFIDCalendar), t.start, t.end, reqStart, reqEnd)
	if err != nil {
		return false, err
	}
	for _, obj := range objs {
		// A read failure is returned: taking it for a free slot would auto-accept
		// the request into a slot the appointment may hold.
		pv, err := st.GetMessageProperties(obj.ID, t.start, t.end, t.busy, t.recur, t.uid)
		if err != nil {
			return false, err
		}
		if blocksWindow(pv, t, reqUID, reqStart, reqEnd) {
			return true, nil
		}
	}
	return seriesConflict(st, t, reqUID, reqStart, reqEnd)
}

// seriesConflict reports whether a recurring appointment occupies the request's
// window. A series master carries its FIRST instance's start, so the window query
// cannot find it: the recurring items are listed on their own and expanded.
func seriesConflict(st *objectstore.Store, t apptTags, reqUID string, reqStart, reqEnd time.Time) (bool, error) {
	objs, err := st.ListFolderObjectsWithFlag(int64(mapi.PrivateFIDCalendar), t.recur)
	if err != nil {
		return false, err
	}
	for _, obj := range objs {
		pv, err := st.GetMessageProperties(obj.ID, t.start, t.end, t.busy, t.uid)
		if err != nil {
			return false, err
		}
		if propStr(pv, t.uid) == reqUID && reqUID != "" {
			continue // the request's own series is not a conflict with itself
		}
		if !mapi.BusyStatusOccupies(longVal(pv, t.busy)) {
			continue
		}
		busy, err := seriesOccupies(st, obj.ID, pv, t, reqStart, reqEnd)
		if err != nil || busy {
			return busy, err
		}
	}
	return false, nil
}

// seriesOccupies reports whether a busy recurring object has an instance in the
// window that occupies it. The series is expanded from what it stores, the
// iCalendar copy or the PidLidAppointmentRecur blob a MAPI client writes alone,
// so an exception that moved an instance or freed it is honoured. A series that
// cannot be expanded still holds the slot its own start and end name: the window
// scan leaves every recurring object to this check.
func seriesOccupies(st *objectstore.Store, id int64, pv mapi.PropertyValues, t apptTags, reqStart, reqEnd time.Time) (bool, error) {
	msg, err := st.OpenMessage(id)
	if err != nil {
		return false, err
	}
	occ, ok, err := oxcical.SeriesOccurrences(msg, oxcical.Options{Resolver: st.GetNamedPropIDs}, reqStart, reqEnd)
	if err != nil {
		return false, err
	}
	if !ok {
		return slotOverlaps(pv, t, reqStart, reqEnd), nil
	}
	for _, o := range occ {
		if mapi.BusyStatusOccupies(o.BusyStatus) && o.Start.Before(reqEnd) && o.End.After(reqStart) {
			return true, nil
		}
	}
	return false, nil
}

// slotOverlaps reports whether an object's own start and end overlap the window.
func slotOverlaps(pv mapi.PropertyValues, t apptTags, reqStart, reqEnd time.Time) bool {
	start, ok1 := ntTime(pv, t.start)
	end, ok2 := ntTime(pv, t.end)
	if !ok1 || !ok2 {
		return false
	}
	// Overlap, not containment.
	return start.Before(reqEnd) && end.After(reqStart)
}

// blocksWindow reports whether one calendar object occupies the request's window.
func blocksWindow(pv mapi.PropertyValues, t apptTags, reqUID string, reqStart, reqEnd time.Time) bool {
	// The request's own prior booking is not a conflict with itself: a meeting
	// update re-sends the same iCal UID, and accepting it updates that appointment
	// in place rather than double-booking against it. The booking path dedups on
	// the same UID; the conflict check must too.
	if reqUID != "" && propStr(pv, t.uid) == reqUID {
		return false
	}
	if boolVal(pv, t.recur) {
		return false // a recurring appointment is judged by seriesConflict, on its instances
	}
	if !mapi.BusyStatusOccupies(longVal(pv, t.busy)) {
		return false // free, and working elsewhere, do not block
	}
	return slotOverlaps(pv, t, reqStart, reqEnd)
}

// ntTime reads a PtSysTime property (stored as an NT timestamp) as a UTC time.
func ntTime(props mapi.PropertyValues, tag mapi.PropTag) (time.Time, bool) {
	if v, ok := props.Get(tag); ok {
		if nt, ok := v.(uint64); ok {
			return mapi.NTTimeToUnix(nt).UTC(), true
		}
	}
	return time.Time{}, false
}

// boolVal reads a PtBoolean property (false when absent).
func boolVal(props mapi.PropertyValues, tag mapi.PropTag) bool {
	v, ok := props.Get(tag)
	if !ok {
		return false
	}
	b, isBool := v.(bool)
	return isBool && b
}

// longVal reads a PtLong property (0 when absent).
func longVal(props mapi.PropertyValues, tag mapi.PropTag) int32 {
	if v, ok := props.Get(tag); ok {
		if n, isLong := v.(int32); isLong {
			return n
		}
	}
	return 0
}
