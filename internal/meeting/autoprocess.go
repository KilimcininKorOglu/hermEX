package meeting

import (
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
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
// not. The booking and notification are delegated to Respond. The just-delivered inbox
// copy is intentionally left in place.
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
	req, err := st.OpenMessage(messageID)
	if err != nil {
		return false, nil // a message that cannot be opened is not auto-processed
	}
	if propStr(req.Props, mapi.PrMessageClass) != "IPM.Schedule.Meeting.Request" {
		return false, nil
	}
	resp, err := decideResponse(st, req, cfg)
	if err != nil {
		return true, err
	}
	// A tentative outcome books the request silently; an accept or decline tells the
	// organizer.
	_, err = Respond(st, accounts, spool, recipient, messageID, resp, resp != ResponseTentative)
	return true, err
}

// apptTags are the appointment named-property tags the auto-processor reads.
type apptTags struct {
	start, end, busy, recur mapi.PropTag
}

// resolveApptTags resolves the appointment start/end/busy/recurring named tags.
func resolveApptTags(st *objectstore.Store) (apptTags, error) {
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole,
		mapi.NameAppointmentEndWhole,
		mapi.NameBusyStatus,
		mapi.NameRecurring,
	})
	if err != nil {
		return apptTags{}, err
	}
	return apptTags{
		start: mapi.MakeTag(ids[0], mapi.PtSysTime),
		end:   mapi.MakeTag(ids[1], mapi.PtSysTime),
		busy:  mapi.MakeTag(ids[2], mapi.PtLong),
		recur: mapi.MakeTag(ids[3], mapi.PtBoolean),
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
// appointment in the Calendar. Conflict detection is blind to recurring series: a
// recurring master is skipped (no instance expansion), so auto-accept can double-book
// against a recurring appointment — a known v1 gap shared with the free/busy view.
// Only a Busy or out-of-office appointment is a hard conflict; free and tentative are
// not.
func hasConflict(st *objectstore.Store, req *oxcmail.Message, t apptTags) (bool, error) {
	reqStart, ok1 := ntTime(req.Props, t.start)
	reqEnd, ok2 := ntTime(req.Props, t.end)
	if !ok1 || !ok2 {
		return false, nil // a request with no time window cannot be judged in conflict
	}
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil {
		return false, err
	}
	for _, obj := range objs {
		pv, err := st.GetMessageProperties(obj.ID, t.start, t.end, t.busy, t.recur)
		if err != nil {
			continue
		}
		if boolVal(pv, t.recur) {
			continue // recurring master: no instance expansion (documented gap)
		}
		if longVal(pv, t.busy) < busyBusy {
			continue // free/tentative does not block
		}
		start, ok1 := ntTime(pv, t.start)
		end, ok2 := ntTime(pv, t.end)
		if !ok1 || !ok2 {
			continue
		}
		// Overlap, not containment.
		if start.Before(reqEnd) && end.After(reqStart) {
			return true, nil
		}
	}
	return false, nil
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
	if v, ok := props.Get(tag); ok {
		b, _ := v.(bool)
		return b
	}
	return false
}

// longVal reads a PtLong property (0 when absent).
func longVal(props mapi.PropertyValues, tag mapi.PropTag) int32 {
	if v, ok := props.Get(tag); ok {
		n, _ := v.(int32)
		return n
	}
	return 0
}
