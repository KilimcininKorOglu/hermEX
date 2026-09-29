package ews

import (
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
	"hermex/internal/oxtask"
)

// taskSetters write one task field UpdateItem names from the <t:Task> its
// SetItemField carries, through the same parsing CreateItem uses.
var taskSetters = map[string]func(tk *oxtask.Task, item createTask) error{
	"item:Subject":    func(tk *oxtask.Task, item createTask) error { tk.Subject = item.Subject; return nil },
	"item:Body":       func(tk *oxtask.Task, item createTask) error { tk.Body = item.textBody(); return nil },
	"item:Categories": func(tk *oxtask.Task, item createTask) error { tk.Categories = item.Categories.String; return nil },
	"item:Importance": func(tk *oxtask.Task, item createTask) error {
		return createTask{Importance: item.Importance}.handling(tk)
	},
	"item:Sensitivity": func(tk *oxtask.Task, item createTask) error {
		return createTask{Sensitivity: item.Sensitivity}.handling(tk)
	},
	"item:ReminderIsSet": func(tk *oxtask.Task, item createTask) error { tk.ReminderSet = item.ReminderIsSet; return nil },
	"item:ReminderDueBy": func(tk *oxtask.Task, item createTask) error {
		return createTask{ReminderDueBy: item.ReminderDueBy}.dates(tk)
	},
	"task:DueDate":   func(tk *oxtask.Task, item createTask) error { return createTask{DueDate: item.DueDate}.dates(tk) },
	"task:StartDate": func(tk *oxtask.Task, item createTask) error { return createTask{StartDate: item.StartDate}.dates(tk) },
	"task:CompleteDate": func(tk *oxtask.Task, item createTask) error {
		return createTask{CompleteDate: item.CompleteDate}.dates(tk)
	},
	"task:Status": func(tk *oxtask.Task, item createTask) error { return createTask{Status: item.Status}.progress(tk) },
	"task:PercentComplete": func(tk *oxtask.Task, item createTask) error {
		return createTask{PercentComplete: item.PercentComplete}.progress(tk)
	},
}

// taskClearers empty one task field a DeleteItemField names.
var taskClearers = map[string]func(tk *oxtask.Task){
	"item:Body":          func(tk *oxtask.Task) { tk.Body = "" },
	"item:Categories":    func(tk *oxtask.Task) { tk.Categories = nil },
	"item:ReminderDueBy": func(tk *oxtask.Task) { tk.ReminderTime = time.Time{} },
	"task:DueDate":       func(tk *oxtask.Task) { tk.Due = time.Time{} },
	"task:StartDate":     func(tk *oxtask.Task) { tk.Start = time.Time{} },
	"task:CompleteDate":  func(tk *oxtask.Task) { tk.DateCompleted = time.Time{} },
}

// isTaskItem reports whether an object-store item is a task.
func isTaskItem(st *objectstore.Store, id int64) bool {
	props, err := st.GetMessageProperties(id, mapi.PrMessageClass)
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.ToUpper(strProp(props, mapi.PrMessageClass)), strings.ToUpper(oxtask.MessageClass))
}

// updateTask applies one ItemChange to a stored task in place, through the
// shared task model: the fields it names are written over the stored task, a
// managed property the edited task no longer sets is removed, and every other
// property stays as another client stored it. The task keeps its id.
func updateTask(st *objectstore.Store, id oxews.ItemID, ch itemChangeReq) itemResponseMessage {
	msg, err := st.OpenMessage(id.MessageID)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}
	tk, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}
	html, code := editTask(&tk, ch)
	if code != "" {
		return itemError(code)
	}
	ext, code := taskExtended(st, ch)
	if code != "" {
		return itemError(code)
	}
	set, removed, err := taskWrite(st, tk)
	if err != nil {
		return itemError("ErrorInvalidPropertySet")
	}
	if html != nil {
		set.Set(mapi.PrHTML, html)
	} else if names(ch, "item:Body") {
		removed = append(removed, mapi.PrHTML)
	}
	ext.apply(&set)
	removed = append(removed, ext.remove...)
	if err := st.ModifyMessageProperties(id.MessageID, set, removed...); err != nil {
		return itemError("ErrorItemSave")
	}
	return itemFound(&itemsWrap{Tasks: []oxews.Task{{ItemID: oxews.ItemIDElem{ID: ch.ItemID.ID, ChangeKey: changeKey(st, id.MessageID)}}}})
}

// editTask writes the fields an ItemChange sets and clears onto the task. html is
// the HTML body a set item:Body carries, which the task model has no field for.
func editTask(tk *oxtask.Task, ch itemChangeReq) (html []byte, code string) {
	for _, sf := range ch.Updates.SetFields {
		if sf.Extended != nil {
			continue
		}
		set, ok := taskSetters[sf.FieldURI.URI]
		if !ok || set(tk, sf.Task) != nil {
			return nil, "ErrorInvalidPropertySet"
		}
		if sf.FieldURI.URI == "item:Body" && strings.EqualFold(sf.Task.Body.Type, "HTML") {
			html = []byte(oxews.ToCRLF(sf.Task.Body.Content))
		}
	}
	for _, df := range ch.Updates.DeleteFields {
		if df.Extended != nil {
			continue
		}
		clear, ok := taskClearers[df.FieldURI.URI]
		if !ok {
			return nil, "ErrorInvalidPropertyDelete"
		}
		clear(tk)
	}
	return html, ""
}

// taskExtended reads the extended-property sets and deletes of an ItemChange on a
// task, leaving out the field deletes editTask applies.
func taskExtended(st *objectstore.Store, ch itemChangeReq) (extUpdate, string) {
	var deletes []deleteItemField
	for _, df := range ch.Updates.DeleteFields {
		if df.Extended != nil {
			deletes = append(deletes, df)
		}
	}
	ch.Updates.DeleteFields = deletes
	return extendedUpdate(st, ch)
}

// taskWrite renders the edited task and lists the managed properties it no longer
// sets, so a cleared field leaves the stored task too.
func taskWrite(st *objectstore.Store, tk oxtask.Task) (mapi.PropertyValues, []mapi.PropTag, error) {
	props, err := oxtask.ToProps(tk, st.GetNamedPropIDs)
	if err != nil {
		return nil, nil, err
	}
	managed, err := oxtask.ManagedTags(st.GetNamedPropIDs)
	if err != nil {
		return nil, nil, err
	}
	var removed []mapi.PropTag
	for _, tag := range managed {
		if !props.Has(tag) {
			removed = append(removed, tag)
		}
	}
	return props, removed, nil
}

// names reports whether an ItemChange sets the field uri.
func names(ch itemChangeReq, uri string) bool {
	for _, sf := range ch.Updates.SetFields {
		if sf.FieldURI.URI == uri {
			return true
		}
	}
	return false
}
