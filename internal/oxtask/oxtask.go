// Package oxtask is the protocol-independent task model. Every surface that exposes
// tasks (webmail, ActiveSync, EWS, CalDAV) converts to and from the one Task shape
// here, mapped onto the MS-OXOTASK named properties, so a single task object is
// identical across every protocol and to a MAPI client (Outlook).
package oxtask

import (
	"time"

	"hermex/internal/mapi"
)

// MessageClass is the store message class for a task object.
const MessageClass = "IPM.Task"

// Resolver maps named properties to store property ids (a store's GetNamedPropIDs).
type Resolver func(create bool, names []mapi.PropertyName) ([]uint16, error)

// Task is the logical task. Zero-value times mean unset; Importance and Sensitivity
// are -1 when unset (PR_IMPORTANCE / PR_SENSITIVITY are 0..2 otherwise).
type Task struct {
	Subject       string
	Body          string
	Start         time.Time
	Due           time.Time
	Complete      bool
	DateCompleted time.Time
	ReminderSet   bool
	ReminderTime  time.Time
	Importance    int
	Sensitivity   int
	Categories    []string
}

// taskNames lists the named properties a task resolves, in a fixed order indexed by
// the idx* constants below.
var taskNames = []mapi.PropertyName{
	mapi.NameTaskStatus,        // PtLong
	mapi.NamePercentComplete,   // PtDouble
	mapi.NameTaskStartDate,     // PtSysTime
	mapi.NameTaskDueDate,       // PtSysTime
	mapi.NameTaskDateCompleted, // PtSysTime
	mapi.NameTaskComplete,      // PtBoolean
	mapi.NameCommonStart,       // PtSysTime
	mapi.NameCommonEnd,         // PtSysTime
	mapi.NameReminderTime,      // PtSysTime
	mapi.NameReminderSet,       // PtBoolean
	mapi.NameKeywords,          // PtMvUnicode
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
)

// New returns a Task with the unset sentinels (Importance/Sensitivity = -1).
func New() Task { return Task{Importance: -1, Sensitivity: -1} }

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
		status := int32(0)
		if t.Complete {
			status = 2 // olComplete
		}
		p.Set(mapi.MakeTag(ids[idxStatus], mapi.PtLong), status)
	}
	if ids[idxPercent] != 0 {
		pct := 0.0
		if t.Complete {
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
	if v, ok := props.Get(named(idxKeywords, mapi.PtMvUnicode)); ok {
		if cats, ok := v.([]string); ok {
			t.Categories = cats
		}
	}
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
