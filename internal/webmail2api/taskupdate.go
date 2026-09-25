package webmail2api

import (
	"errors"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxtask"
)

// errNoSuchTask reports an update aimed at an id that holds no task.
var errNoSuchTask = errors.New("webmail2api: no such task")

// updateTaskInPlace writes the editor's fields over a stored task and keeps it
// the same object: the message id and every property and attachment the task
// model does not cover stay as another client stored them. Fields the editor
// does not surface keep their stored values, and a managed property the merged
// task no longer sets is removed. It returns the merged task.
func updateTaskInPlace(st *objectstore.Store, id int64, edit oxtask.Task) (oxtask.Task, error) {
	stored, err := st.OpenMessage(id)
	if errors.Is(err, objectstore.ErrNotFound) || (err == nil && !isTask(stored.Props)) {
		return oxtask.Task{}, errNoSuchTask
	}
	if err != nil {
		return oxtask.Task{}, err
	}
	prev, err := oxtask.FromProps(stored.Props, st.GetNamedPropIDs)
	if err != nil {
		return oxtask.Task{}, err
	}
	merged := mergeTaskEdit(prev, edit)
	props, err := oxtask.ToProps(merged, st.GetNamedPropIDs)
	if err != nil {
		return oxtask.Task{}, err
	}
	managed, err := oxtask.ManagedTags(st.GetNamedPropIDs)
	if err != nil {
		return oxtask.Task{}, err
	}
	return merged, st.ModifyMessageProperties(id, props, absentTags(managed, props)...)
}

// mergeTaskEdit applies the fields the editor surfaces onto the stored task, so
// the fields it does not surface (the completion date, the reminder time, the
// sensitivity, the assignment bookkeeping) keep what another protocol set.
func mergeTaskEdit(prev, edit oxtask.Task) oxtask.Task {
	prev.Subject = edit.Subject
	prev.Body = edit.Body
	prev.Complete = edit.Complete
	prev.Due = edit.Due
	prev.Start = edit.Start
	// An edit that names no priority keeps the stored one.
	if edit.Importance >= 0 {
		prev.Importance = edit.Importance
	}
	prev.ReminderSet = edit.ReminderSet
	prev.Categories = edit.Categories
	prev.Status = edit.Status
	prev.PercentComplete = edit.PercentComplete
	prev.RecurrenceRule = edit.RecurrenceRule
	prev.Owner = edit.Owner
	prev.Assigner = edit.Assigner
	prev.AcceptanceState = edit.AcceptanceState
	return prev
}

// isTask reports whether a stored item is a task.
func isTask(props mapi.PropertyValues) bool {
	return strings.HasPrefix(strings.ToUpper(propStr(props, mapi.PrMessageClass)), strings.ToUpper(oxtask.MessageClass))
}
