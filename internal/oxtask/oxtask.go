// Package oxtask is the protocol-independent task model. Every surface that exposes
// tasks (webmail, ActiveSync, EWS, CalDAV) converts to and from the one Task shape
// here, mapped onto the MS-OXOTASK named properties, so a single task object is
// identical across every protocol and to a MAPI client (Outlook).
package oxtask

import (
	"math"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/recurrence"
)

// fitsLong reports whether a model integer fits the 32-bit MAPI long it is about
// to be written into. The model's ints come from protocol mappers fed by client
// input (a JSON body, a WBXML element, a SOAP element), none of which is bound by
// that width, so a value past it would wrap into a different valid setting.
func fitsLong(v int) bool { return v >= math.MinInt32 && v <= math.MaxInt32 }

// MessageClass is the store message class for a task object.
const MessageClass = "IPM.Task"

// Resolver maps named properties to store property ids (a store's GetNamedPropIDs).
type Resolver func(create bool, names []mapi.PropertyName) ([]uint16, error)

// Task is the logical task. Zero-value times mean unset; Importance and Sensitivity
// are -1 when unset (PR_IMPORTANCE / PR_SENSITIVITY are 0..2 otherwise). Status is
// -1 when unset (0=not started, 1=in progress, 2=complete, 3=waiting, 4=deferred);
// PercentComplete is -1 when unset (0.0..1.0 otherwise). When Status/Percent are
// unset, ToProps derives them from Complete for backward compatibility.
type Task struct {
	Subject         string
	Body            string
	Start           time.Time
	Due             time.Time
	Complete        bool
	DateCompleted   time.Time
	Status          int // -1 unset, else 0..4 (PidLidTaskStatus)
	PercentComplete float64
	ReminderSet     bool
	ReminderTime    time.Time
	Importance      int
	Sensitivity     int
	Categories      []string
	RecurrenceRule  string    // RRULE string (webmail-only recurrence; see mapi.NameTaskRecurrenceRule)
	Owner           string    // PidLidTaskOwner (current keeper), "" when unassigned
	Assigner        string    // PidLidTaskAssigner (last assigner), "" on the assigner's own copy
	AcceptanceState int       // PidLidTaskAcceptanceState: -1 unset, 0 not assigned, 1 unknown, 2 accepted, 3 rejected
	FCreator        bool      // PidLidTaskFCreator: this copy belongs to the original creator
	LastUpdate      time.Time // PidLidTaskLastUpdate (assignment-related change time)
}

// taskNames lists the named properties a task resolves, in a fixed order indexed by
// the idx* constants below.
var taskNames = []mapi.PropertyName{
	mapi.NameTaskStatus,          // PtLong
	mapi.NamePercentComplete,     // PtDouble
	mapi.NameTaskStartDate,       // PtSysTime
	mapi.NameTaskDueDate,         // PtSysTime
	mapi.NameTaskDateCompleted,   // PtSysTime
	mapi.NameTaskComplete,        // PtBoolean
	mapi.NameCommonStart,         // PtSysTime
	mapi.NameCommonEnd,           // PtSysTime
	mapi.NameReminderTime,        // PtSysTime
	mapi.NameReminderSet,         // PtBoolean
	mapi.NameKeywords,            // PtMvUnicode
	mapi.NameTaskRecurrenceRule,  // PtUnicode (RRULE string; webmail-only, see mapi.NameTaskRecurrenceRule)
	mapi.NameTaskOwner,           // PtUnicode
	mapi.NameTaskAssigner,        // PtUnicode
	mapi.NameTaskAcceptanceState, // PtLong
	mapi.NameTaskFCreator,        // PtBoolean
	mapi.NameTaskLastUpdate,      // PtSysTime
	mapi.NameTaskRecurrence,      // PtBinary (MS-OXOCAL RecurrencePattern blob)
}

const (
	idxStatus = iota
	idxPercent
	idxStartDate
	idxDueDate
	idxDateCompleted
	idxComplete
	idxCommonStart
	idxCommonEnd
	idxReminderTime
	idxReminderSet
	idxKeywords
	idxRecurrenceRule
	idxOwner
	idxAssigner
	idxAcceptanceState
	idxFCreator
	idxLastUpdate
	idxTaskRecurrence
)

// New returns a Task with the unset sentinels (Importance/Sensitivity/Status = -1,
// PercentComplete = -1, AcceptanceState = -1).
func New() Task {
	return Task{Importance: -1, Sensitivity: -1, Status: -1, PercentComplete: -1, AcceptanceState: -1}
}

// ToProps renders a task to MAPI properties, allocating the named-property ids.
func ToProps(t Task, resolve Resolver) (mapi.PropertyValues, error) {
	ids, err := resolve(true, taskNames)
	if err != nil {
		return nil, err
	}
	var p mapi.PropertyValues
	w := taskWriter{p: &p, ids: ids}
	w.writeCore(t)
	w.writeSchedule(t)
	w.writeProgress(t)
	w.writeReminder(t)
	w.writeCategories(t)
	w.writeRecurrence(t)
	w.writeAssignment(t)
	return p, nil
}

// taskWriter accumulates a task's properties. Every named-property write is skipped
// when the store allocated no id for that name.
type taskWriter struct {
	p   *mapi.PropertyValues
	ids []uint16
}

// time writes a named PtSysTime property, skipping an unset instant.
func (w taskWriter) time(idx int, when time.Time) {
	if w.ids[idx] != 0 && !when.IsZero() {
		w.p.Set(mapi.MakeTag(w.ids[idx], mapi.PtSysTime), mapi.UnixToNTTime(when))
	}
}

// boolean writes a named PtBoolean property.
func (w taskWriter) boolean(idx int, v bool) {
	if w.ids[idx] != 0 {
		w.p.Set(mapi.MakeTag(w.ids[idx], mapi.PtBoolean), v)
	}
}

// long writes a named PtLong property.
func (w taskWriter) long(idx int, v int32) {
	if w.ids[idx] != 0 {
		w.p.Set(mapi.MakeTag(w.ids[idx], mapi.PtLong), v)
	}
}

// double writes a named PtDouble property.
func (w taskWriter) double(idx int, v float64) {
	if w.ids[idx] != 0 {
		w.p.Set(mapi.MakeTag(w.ids[idx], mapi.PtDouble), v)
	}
}

// text writes a named PtUnicode property, skipping an empty value.
func (w taskWriter) text(idx int, s string) {
	if s != "" && w.ids[idx] != 0 {
		w.p.Set(mapi.MakeTag(w.ids[idx], mapi.PtUnicode), s)
	}
}

// writeCore writes the message class, the subject/body and the two handling levels.
func (w taskWriter) writeCore(t Task) {
	w.p.Set(mapi.PrMessageClass, MessageClass)
	w.p.Set(mapi.PrSubject, t.Subject)
	w.p.Set(mapi.PrBody, t.Body)
	if t.Importance >= 0 && fitsLong(t.Importance) {
		// #nosec G115 -- the guard on the same line refuses a value the property cannot carry
		w.p.Set(mapi.PrImportance, int32(t.Importance))
	}
	if t.Sensitivity >= 0 && fitsLong(t.Sensitivity) {
		// #nosec G115 -- the guard on the same line refuses a value the property cannot carry
		w.p.Set(mapi.PrSensitivity, int32(t.Sensitivity))
	}
}

// writeSchedule writes the start and due instants to both the task-specific and the
// common date properties.
func (w taskWriter) writeSchedule(t Task) {
	w.time(idxStartDate, t.Start)
	w.time(idxCommonStart, t.Start)
	w.time(idxDueDate, t.Due)
	w.time(idxCommonEnd, t.Due)
}

// writeProgress writes the completion flag, the status, the percentage and the
// completion date.
func (w taskWriter) writeProgress(t Task) {
	w.boolean(idxComplete, t.Complete)
	w.long(idxStatus, taskStatus(t))
	w.double(idxPercent, taskPercent(t))
	if t.Complete {
		w.time(idxDateCompleted, t.DateCompleted)
	}
}

// taskStatus is the status to store: the model's own value when set, otherwise
// derived from Complete.
func taskStatus(t Task) int32 {
	if t.Status >= 0 && fitsLong(t.Status) {
		// #nosec G115 -- the guard on the line above refuses a value the property cannot carry
		return int32(t.Status)
	}
	if t.Complete {
		return 2 // olComplete
	}
	return 0
}

// taskPercent is the percentage to store: the model's own value when set, otherwise
// derived from Complete.
func taskPercent(t Task) float64 {
	if t.PercentComplete >= 0 {
		return t.PercentComplete
	}
	if t.Complete {
		return 1.0
	}
	return 0
}

// writeReminder writes the reminder flag, plus the instant when one is set.
func (w taskWriter) writeReminder(t Task) {
	w.boolean(idxReminderSet, t.ReminderSet)
	if t.ReminderSet {
		w.time(idxReminderTime, t.ReminderTime)
	}
}

// writeCategories writes the keyword list when the task carries one.
func (w taskWriter) writeCategories(t Task) {
	if len(t.Categories) > 0 && w.ids[idxKeywords] != 0 {
		w.p.Set(mapi.MakeTag(w.ids[idxKeywords], mapi.PtMvUnicode), t.Categories)
	}
}

// writeRecurrence writes the RRULE text and the MS-OXOCAL RecurrencePattern blob
// Outlook reads for a recurring task. The series anchor is the task start (falling
// back to the due date so a due-only recurring task still emits a valid blob); a blob
// is emitted only when the anchor is set.
func (w taskWriter) writeRecurrence(t Task) {
	if t.RecurrenceRule == "" {
		return
	}
	w.text(idxRecurrenceRule, t.RecurrenceRule)
	if w.ids[idxTaskRecurrence] == 0 {
		return
	}
	anchor := t.Start
	if anchor.IsZero() {
		anchor = t.Due
	}
	if anchor.IsZero() {
		return
	}
	if blob, err := recurrence.FromRRule(t.RecurrenceRule, anchor); err == nil {
		w.p.Set(mapi.MakeTag(w.ids[idxTaskRecurrence], mapi.PtBinary), blob)
	}
}

// writeAssignment writes the assignment fields: the current keeper, the last
// assigner, the acceptance state, the creator flag and the last update instant.
func (w taskWriter) writeAssignment(t Task) {
	w.text(idxOwner, t.Owner)
	w.text(idxAssigner, t.Assigner)
	w.long(idxAcceptanceState, taskAcceptance(t))
	w.boolean(idxFCreator, t.FCreator)
	w.time(idxLastUpdate, t.LastUpdate)
}

// taskAcceptance is the acceptance state to store: the model's own value when set,
// otherwise 0 (not assigned), the default Outlook writes for an unassigned task.
func taskAcceptance(t Task) int32 {
	if t.AcceptanceState >= 0 && fitsLong(t.AcceptanceState) {
		// #nosec G115 -- the guard on the line above refuses a value the property cannot carry
		return int32(t.AcceptanceState)
	}
	return 0
}

// FromProps reads a task from a message's properties.
func FromProps(props mapi.PropertyValues, resolve Resolver) (Task, error) {
	ids, err := resolve(false, taskNames)
	if err != nil {
		return Task{}, err
	}
	r := taskReader{props: props, ids: ids}
	t := New()
	r.readCore(&t)
	r.readSchedule(&t)
	r.readProgress(&t)
	r.readCategories(&t)
	r.readRecurrence(&t)
	r.readAssignment(&t)
	return t, nil
}

// taskReader reads a task's properties. A named property the store has no id for
// reads as absent.
type taskReader struct {
	props mapi.PropertyValues
	ids   []uint16
}

// named returns the tag of a named property, or 0 when the store has no id for it.
func (r taskReader) named(idx int, ty mapi.PropType) mapi.PropTag {
	if r.ids[idx] == 0 {
		return 0
	}
	return mapi.MakeTag(r.ids[idx], ty)
}

// readCore reads the subject, the body and the two handling levels.
func (r taskReader) readCore(t *Task) {
	t.Subject = strProp(r.props, mapi.PrSubject)
	t.Body = strProp(r.props, mapi.PrBody)
	if v, ok := longProp(r.props, mapi.PrImportance); ok {
		t.Importance = v
	}
	if v, ok := longProp(r.props, mapi.PrSensitivity); ok {
		t.Sensitivity = v
	}
}

// readSchedule reads the start, the due date and the reminder, preferring the
// task-specific date and falling back to the common one.
func (r taskReader) readSchedule(t *Task) {
	t.Start = firstTime(r.props, r.named(idxStartDate, mapi.PtSysTime), r.named(idxCommonStart, mapi.PtSysTime))
	t.Due = firstTime(r.props, r.named(idxDueDate, mapi.PtSysTime), r.named(idxCommonEnd, mapi.PtSysTime))
	t.ReminderSet = boolProp(r.props, r.named(idxReminderSet, mapi.PtBoolean))
	t.ReminderTime = timeProp(r.props, r.named(idxReminderTime, mapi.PtSysTime))
}

// readProgress reads the completion flag, the completion date, the status and the
// percentage.
func (r taskReader) readProgress(t *Task) {
	t.Complete = boolProp(r.props, r.named(idxComplete, mapi.PtBoolean))
	t.DateCompleted = timeProp(r.props, r.named(idxDateCompleted, mapi.PtSysTime))
	if v, ok := longProp(r.props, r.named(idxStatus, mapi.PtLong)); ok {
		t.Status = v
	}
	if pct, ok := typedProp[float64](r.props, r.named(idxPercent, mapi.PtDouble)); ok {
		t.PercentComplete = pct
	}
}

// readCategories reads the keyword list.
func (r taskReader) readCategories(t *Task) {
	if cats, ok := typedProp[[]string](r.props, r.named(idxKeywords, mapi.PtMvUnicode)); ok {
		t.Categories = cats
	}
}

// readRecurrence reads the stored RRULE text. When none is stored (a MAPI client
// authored the task and wrote only the MS-OXOCAL blob), it decodes the blob back to
// the RRULE so the EAS/webmail paths read the same recurrence a MAPI client wrote.
func (r taskReader) readRecurrence(t *Task) {
	if s, ok := typedProp[string](r.props, r.named(idxRecurrenceRule, mapi.PtUnicode)); ok {
		t.RecurrenceRule = s
	}
	if t.RecurrenceRule != "" {
		return
	}
	blob, ok := typedProp[[]byte](r.props, r.named(idxTaskRecurrence, mapi.PtBinary))
	if !ok {
		return
	}
	if rule, ok := recurrence.ToRRule(blob); ok {
		t.RecurrenceRule = rule
	}
}

// readAssignment reads the current keeper, the last assigner, the acceptance state,
// the creator flag and the last update instant.
func (r taskReader) readAssignment(t *Task) {
	t.Owner = strProp(r.props, r.named(idxOwner, mapi.PtUnicode))
	t.Assigner = strProp(r.props, r.named(idxAssigner, mapi.PtUnicode))
	if v, ok := longProp(r.props, r.named(idxAcceptanceState, mapi.PtLong)); ok {
		t.AcceptanceState = v
	}
	t.FCreator = boolProp(r.props, r.named(idxFCreator, mapi.PtBoolean))
	t.LastUpdate = timeProp(r.props, r.named(idxLastUpdate, mapi.PtSysTime))
}

// typedProp returns a property's value when it is present and carries type T.
func typedProp[T any](p mapi.PropertyValues, tag mapi.PropTag) (T, bool) {
	if v, ok := p.Get(tag); ok {
		if tv, ok := v.(T); ok {
			return tv, true
		}
	}
	var zero T
	return zero, false
}

func strProp(p mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := p.Get(tag); ok {
		switch s := v.(type) {
		case string:
			return s
		case []byte:
			return string(s)
		}
	}
	return ""
}

func longProp(p mapi.PropertyValues, tag mapi.PropTag) (int, bool) {
	if v, ok := p.Get(tag); ok {
		if n, ok := v.(int32); ok {
			return int(n), true
		}
	}
	return 0, false
}

func boolProp(p mapi.PropertyValues, tag mapi.PropTag) bool {
	if tag == 0 {
		return false
	}
	if v, ok := p.Get(tag); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func timeProp(p mapi.PropertyValues, tag mapi.PropTag) time.Time {
	if tag == 0 {
		return time.Time{}
	}
	if v, ok := p.Get(tag); ok {
		if nt, ok := v.(uint64); ok {
			return mapi.NTTimeToUnix(nt).UTC()
		}
	}
	return time.Time{}
}

func firstTime(p mapi.PropertyValues, primary, fallback mapi.PropTag) time.Time {
	if t := timeProp(p, primary); !t.IsZero() {
		return t
	}
	return timeProp(p, fallback)
}
