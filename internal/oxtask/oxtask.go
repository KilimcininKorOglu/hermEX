// Package oxtask is the protocol-independent task model. Every surface that exposes
// tasks (webmail, ActiveSync, EWS, CalDAV) converts to and from the one Task shape
// here, mapped onto the MS-OXOTASK named properties, so a single task object is
// identical across every protocol and to a MAPI client (Outlook).
package oxtask

import (
	"time"

	"hermex/internal/mapi"
	"hermex/internal/recurrence"
)

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
	p.Set(mapi.PrMessageClass, MessageClass)
	p.Set(mapi.PrSubject, t.Subject)
	p.Set(mapi.PrBody, t.Body)
	if t.Importance >= 0 {
		p.Set(mapi.PrImportance, int32(t.Importance))
	}
	if t.Sensitivity >= 0 {
		p.Set(mapi.PrSensitivity, int32(t.Sensitivity))
	}
	setTime := func(idx int, when time.Time) {
		if ids[idx] != 0 && !when.IsZero() {
			p.Set(mapi.MakeTag(ids[idx], mapi.PtSysTime), mapi.UnixToNTTime(when))
		}
	}
	setBool := func(idx int, v bool) {
		if ids[idx] != 0 {
			p.Set(mapi.MakeTag(ids[idx], mapi.PtBoolean), v)
		}
	}
	setTime(idxStartDate, t.Start)
	setTime(idxCommonStart, t.Start)
	setTime(idxDueDate, t.Due)
	setTime(idxCommonEnd, t.Due)
	setBool(idxComplete, t.Complete)
	if ids[idxStatus] != 0 {
		// Status takes precedence when set; otherwise derive from Complete.
		status := int32(0)
		if t.Status >= 0 {
			status = int32(t.Status)
		} else if t.Complete {
			status = 2 // olComplete
		}
		p.Set(mapi.MakeTag(ids[idxStatus], mapi.PtLong), status)
	}
	if ids[idxPercent] != 0 {
		// Percent takes precedence when set; otherwise derive from Complete.
		pct := 0.0
		if t.PercentComplete >= 0 {
			pct = t.PercentComplete
		} else if t.Complete {
			pct = 1.0
		}
		p.Set(mapi.MakeTag(ids[idxPercent], mapi.PtDouble), pct)
	}
	if t.Complete {
		setTime(idxDateCompleted, t.DateCompleted)
	}
	setBool(idxReminderSet, t.ReminderSet)
	if t.ReminderSet {
		setTime(idxReminderTime, t.ReminderTime)
	}
	if len(t.Categories) > 0 && ids[idxKeywords] != 0 {
		p.Set(mapi.MakeTag(ids[idxKeywords], mapi.PtMvUnicode), t.Categories)
	}
	if t.RecurrenceRule != "" && ids[idxRecurrenceRule] != 0 {
		p.Set(mapi.MakeTag(ids[idxRecurrenceRule], mapi.PtUnicode), t.RecurrenceRule)
	}
	// The MS-OXOCAL RecurrencePattern blob Outlook reads for a recurring task. The
	// series anchor is the task start (fall back to due/now so a due-only recurring
	// task still emits a valid blob); a blob is emitted only when the anchor is set.
	if t.RecurrenceRule != "" && ids[idxTaskRecurrence] != 0 {
		anchor := t.Start
		if anchor.IsZero() {
			anchor = t.Due
		}
		if !anchor.IsZero() {
			if blob, err := recurrence.FromRRule(t.RecurrenceRule, anchor); err == nil {
				p.Set(mapi.MakeTag(ids[idxTaskRecurrence], mapi.PtBinary), blob)
			}
		}
	}
	if t.Owner != "" && ids[idxOwner] != 0 {
		p.Set(mapi.MakeTag(ids[idxOwner], mapi.PtUnicode), t.Owner)
	}
	if t.Assigner != "" && ids[idxAssigner] != 0 {
		p.Set(mapi.MakeTag(ids[idxAssigner], mapi.PtUnicode), t.Assigner)
	}
	if ids[idxAcceptanceState] != 0 {
		// AcceptanceState takes precedence when set; otherwise 0 (not assigned) is
		// the default Outlook writes for an unassigned task.
		state := int32(0)
		if t.AcceptanceState >= 0 {
			state = int32(t.AcceptanceState)
		}
		p.Set(mapi.MakeTag(ids[idxAcceptanceState], mapi.PtLong), state)
	}
	if ids[idxFCreator] != 0 {
		p.Set(mapi.MakeTag(ids[idxFCreator], mapi.PtBoolean), t.FCreator)
	}
	if ids[idxLastUpdate] != 0 && !t.LastUpdate.IsZero() {
		p.Set(mapi.MakeTag(ids[idxLastUpdate], mapi.PtSysTime), mapi.UnixToNTTime(t.LastUpdate))
	}
	return p, nil
}

// FromProps reads a task from a message's properties.
func FromProps(props mapi.PropertyValues, resolve Resolver) (Task, error) {
	ids, err := resolve(false, taskNames)
	if err != nil {
		return Task{}, err
	}
	t := New()
	t.Subject = strProp(props, mapi.PrSubject)
	t.Body = strProp(props, mapi.PrBody)
	if v, ok := longProp(props, mapi.PrImportance); ok {
		t.Importance = v
	}
	if v, ok := longProp(props, mapi.PrSensitivity); ok {
		t.Sensitivity = v
	}
	named := func(idx int, ty mapi.PropType) mapi.PropTag {
		if ids[idx] == 0 {
			return 0
		}
		return mapi.MakeTag(ids[idx], ty)
	}
	// Prefer the task-specific date, fall back to the common one.
	t.Start = firstTime(props, named(idxStartDate, mapi.PtSysTime), named(idxCommonStart, mapi.PtSysTime))
	t.Due = firstTime(props, named(idxDueDate, mapi.PtSysTime), named(idxCommonEnd, mapi.PtSysTime))
	t.Complete = boolProp(props, named(idxComplete, mapi.PtBoolean))
	t.DateCompleted = timeProp(props, named(idxDateCompleted, mapi.PtSysTime))
	t.ReminderSet = boolProp(props, named(idxReminderSet, mapi.PtBoolean))
	t.ReminderTime = timeProp(props, named(idxReminderTime, mapi.PtSysTime))
	if v, ok := longProp(props, named(idxStatus, mapi.PtLong)); ok {
		t.Status = v
	}
	if v, ok := props.Get(named(idxPercent, mapi.PtDouble)); ok {
		if pct, ok := v.(float64); ok {
			t.PercentComplete = pct
		}
	}
	if v, ok := props.Get(named(idxKeywords, mapi.PtMvUnicode)); ok {
		if cats, ok := v.([]string); ok {
			t.Categories = cats
		}
	}
	if v, ok := props.Get(named(idxRecurrenceRule, mapi.PtUnicode)); ok {
		if s, ok := v.(string); ok {
			t.RecurrenceRule = s
		}
	}
	t.Owner = strProp(props, named(idxOwner, mapi.PtUnicode))
	t.Assigner = strProp(props, named(idxAssigner, mapi.PtUnicode))
	if v, ok := longProp(props, named(idxAcceptanceState, mapi.PtLong)); ok {
		t.AcceptanceState = v
	}
	t.FCreator = boolProp(props, named(idxFCreator, mapi.PtBoolean))
	t.LastUpdate = timeProp(props, named(idxLastUpdate, mapi.PtSysTime))
	return t, nil
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
