package webmail2api

import (
	"net/http"
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxtask"
)

// reminderJSON is one due reminder the SPA's ReminderDialog lists. The id is the
// object-store message id; type tells the dialog whether the reminder is for an
// appointment or a task so it can open the right editor. dueTime is when the
// reminder fires (PidLidReminderTime), start is the appointment/task start.
type reminderJSON struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Type    string `json:"type"`            // "appointment" or "task"
	DueTime string `json:"dueTime"`         // RFC3339, when the reminder fires
	Start   string `json:"start,omitempty"` // the appointment/task start
}

// handleGetReminders lists the due reminders across the calendar and Tasks folders:
// every appointment or task with PidLidReminderSet=true whose PidLidReminderTime has
// passed (the MS-OXOCAL "FlagDueBy <= now" rule, simplified to non-recurring for v1).
// Mirrors the reference reminderlistmodule's enumerate-then-filter, capped at 99.
func (s *Server) handleGetReminders(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	now := time.Now().UTC()
	reminders := listReminders(st, mapi.PrivateFIDCalendar, "appointment", now)
	reminders = append(reminders, listReminders(st, mapi.PrivateFIDTasks, "task", now)...)
	writeJSON(w, http.StatusOK, map[string]any{"reminders": reminders})
}

// listReminders enumerates one folder's objects and returns those with a fired
// reminder. Appointment reminders read PidLidReminderSet/Time from the named props
// oxcical stores; task reminders reuse oxtask.FromProps.
func listReminders(st *objectstore.Store, folderID int64, kind string, now time.Time) []reminderJSON {
	objs, _ := st.ListFolderObjects(folderID)
	out := make([]reminderJSON, 0, len(objs))
	for _, o := range objs {
		msg, err := st.OpenMessage(o.ID)
		if err != nil {
			continue
		}
		var due time.Time
		var subject, startStr string
		if kind == "appointment" {
			due, subject, startStr = appointmentReminder(st, msg)
		} else {
			due, subject, startStr = taskReminder(st, msg)
		}
		if due.IsZero() || due.After(now) {
			continue
		}
		out = append(out, reminderJSON{
			ID:      strconv.FormatInt(o.ID, 10),
			Subject: subject,
			Type:    kind,
			DueTime: due.UTC().Format(time.RFC3339),
			Start:   startStr,
		})
		if len(out) >= 99 {
			break
		}
	}
	return out
}

// appointmentReminder reads a calendar item's reminder; a zero time means no due
// reminder. The fire instant is PidLidReminderTime when set, else the appointment
// start minus PidLidReminderDelta (the VALARM lead time oxcical stores), the same
// FlagDueBy fallback the reference reminderlistmodule applies.
func appointmentReminder(st *objectstore.Store, msg *oxcmail.Message) (due time.Time, subject, start string) {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameReminderSet, mapi.NameReminderTime, mapi.NameReminderDelta, mapi.NameAppointmentStartWhole})
	if err != nil || len(ids) < 4 {
		return
	}
	setTag := mapi.MakeTag(ids[0], mapi.PtBoolean)
	timeTag := mapi.MakeTag(ids[1], mapi.PtSysTime)
	deltaTag := mapi.MakeTag(ids[2], mapi.PtLong)
	startTag := mapi.MakeTag(ids[3], mapi.PtSysTime)
	if b, ok := msg.Props.Get(setTag); !ok || !asBool(b) {
		return
	}
	due = timeProp(msg.Props, timeTag)
	startT := timeProp(msg.Props, startTag)
	if due.IsZero() && !startT.IsZero() {
		delta := 15 * time.Minute // Outlook default for calendar
		if v, ok := msg.Props.Get(deltaTag); ok {
			if n, ok := v.(int32); ok && n > 0 {
				delta = time.Duration(n) * time.Minute
			}
		}
		due = startT.Add(-delta)
	}
	subject = strProp(msg.Props, mapi.PrSubject)
	if !startT.IsZero() {
		start = startT.UTC().Format(time.RFC3339)
	}
	return
}

// taskReminder reads a task's reminder through the oxtask model. The fire instant is
// PidLidReminderTime when set, else the task Due (Outlook's task reminder defaults to
// the due time).
func taskReminder(st *objectstore.Store, msg *oxcmail.Message) (due time.Time, subject, start string) {
	t, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
	if err != nil || !t.ReminderSet {
		return
	}
	due = t.ReminderTime
	if due.IsZero() {
		due = t.Due
	}
	subject = t.Subject
	if !t.Start.IsZero() {
		start = t.Start.UTC().Format(time.RFC3339)
	}
	return
}

// asBool unwraps a MAPI boolean (stored as Go bool).
func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// timeProp reads a PidLidReminderTime-style PtSysTime from a property bag.
func timeProp(p mapi.PropertyValues, tag mapi.PropTag) time.Time {
	if tag == 0 {
		return time.Time{}
	}
	v, ok := p.Get(tag)
	if !ok {
		return time.Time{}
	}
	nt, ok := v.(uint64)
	if !ok {
		return time.Time{}
	}
	return mapi.NTTimeToUnix(nt).UTC()
}

// strProp reads a string property (string or []byte) from a property bag.
func strProp(p mapi.PropertyValues, tag mapi.PropTag) string {
	v, ok := p.Get(tag)
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	}
	return ""
}

// handleDismissReminder clears a reminder: PidLidReminderSet=false on the message.
// Recurring-series next-instance advancement is a follow-on (v1 dismisses the whole
// reminder, matching the non-recurring path of the reference dismissItem).
func (s *Server) handleDismissReminder(w http.ResponseWriter, r *http.Request) {
	s.mutateReminder(w, r, func(due time.Time) (mapi.PropertyValues, []mapi.PropTag) {
		var p mapi.PropertyValues
		p.Set(mapi.MakeTag(0, mapi.PtBoolean), false) // tag filled by caller
		return p, nil
	}, false)
}

// handleSnoozeReminder postpones a reminder by the requested minutes: PidLidReminderTime
// advances to now+minutes while PidLidReminderSet stays true.
func (s *Server) handleSnoozeReminder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Minutes int `json:"minutes"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Minutes <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "minutes required"})
		return
	}
	delta := time.Duration(req.Minutes) * time.Minute
	s.mutateReminder(w, r, func(due time.Time) (mapi.PropertyValues, []mapi.PropTag) {
		if due.IsZero() {
			due = time.Now().UTC()
		}
		var p mapi.PropertyValues
		p.Set(mapi.MakeTag(0, mapi.PtSysTime), mapi.UnixToNTTime(due.Add(delta)))
		return p, nil
	}, true)
}

// mutateReminder resolves the message's reminder named props, applies the builder to
// produce the new props (dismissing clears the flag, snoozing advances the time), and
// writes them back. keepSet true leaves ReminderSet as-is (snooze); false sets it false
// (dismiss). The builder receives a zero MakeTag placeholder; the real tags are
// substituted here from the store's resolver.
func (s *Server) mutateReminder(w http.ResponseWriter, r *http.Request, build func(due time.Time) (mapi.PropertyValues, []mapi.PropTag), keepSet bool) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	msg, err := st.OpenMessage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "reminder not found"})
		return
	}
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameReminderSet, mapi.NameReminderTime})
	if err != nil || len(ids) < 2 || ids[0] == 0 || ids[1] == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not resolve reminder props"})
		return
	}
	setTag := mapi.MakeTag(ids[0], mapi.PtBoolean)
	timeTag := mapi.MakeTag(ids[1], mapi.PtSysTime)
	due := timeProp(msg.Props, timeTag)
	props, deletes := build(due)
	var out mapi.PropertyValues
	for _, tp := range props {
		switch tp.Tag.Type() {
		case mapi.PtBoolean:
			out.Set(setTag, tp.Value)
		case mapi.PtSysTime:
			out.Set(timeTag, tp.Value)
		default:
			out.Set(tp.Tag, tp.Value)
		}
	}
	if !keepSet {
		out.Set(setTag, false)
	}
	if err := st.ModifyMessageProperties(id, out, deletes...); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save reminder"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
