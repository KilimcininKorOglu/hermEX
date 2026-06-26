package activesync

import (
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxtask"
	"hermex/internal/wbxml"
)

// MS-ASTASK: a stored IPM.Task maps to and from the AirSync Tasks class through the
// shared oxtask model, so a task is the same object across webmail, ActiveSync, EWS,
// and a MAPI client. Recurrence is carried by the store but not yet surfaced here.

// taskAppData builds the AirSync ApplicationData for a stored task.
func taskAppData(st *objectstore.Store, objectID int64) (*wbxml.Node, error) {
	msg, err := st.OpenMessage(objectID)
	if err != nil {
		return nil, err
	}
	if contactStr(msg.Props, mapi.PrMessageClass) != oxtask.MessageClass {
		return nil, nil // not a task; nothing to stream
	}
	t, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
	if err != nil {
		return nil, err
	}
	data := wbxml.Elem(wbxml.ASData)
	if t.Subject != "" {
		data.Children = append(data.Children, wbxml.Str(wbxml.TKSubject, t.Subject))
	}
	if t.Body != "" {
		data.Children = append(data.Children, wbxml.Elem(wbxml.ABBody,
			wbxml.Str(wbxml.ABType, "1"),
			wbxml.Str(wbxml.ABEstimatedDataSize, strconv.Itoa(len(t.Body))),
			wbxml.Opaque(wbxml.ABData, []byte(t.Body))))
	}
	if t.Importance >= 0 {
		data.Children = append(data.Children, wbxml.Str(wbxml.TKImportance, strconv.Itoa(t.Importance)))
	}
	if t.Sensitivity >= 0 {
		data.Children = append(data.Children, wbxml.Str(wbxml.TKSensitivity, strconv.Itoa(t.Sensitivity)))
	}
	if !t.Start.IsZero() {
		s := t.Start.UTC().Format(easContactDate)
		data.Children = append(data.Children, wbxml.Str(wbxml.TKStartDate, s), wbxml.Str(wbxml.TKUtcStartDate, s))
	}
	if !t.Due.IsZero() {
		s := t.Due.UTC().Format(easContactDate)
		data.Children = append(data.Children, wbxml.Str(wbxml.TKDueDate, s), wbxml.Str(wbxml.TKUtcDueDate, s))
	}
	data.Children = append(data.Children, wbxml.Str(wbxml.TKComplete, boolStr(t.Complete)))
	if t.Complete && !t.DateCompleted.IsZero() {
		data.Children = append(data.Children, wbxml.Str(wbxml.TKDateCompleted, t.DateCompleted.UTC().Format(easContactDate)))
	}
	data.Children = append(data.Children, wbxml.Str(wbxml.TKReminderSet, boolStr(t.ReminderSet)))
	if t.ReminderSet && !t.ReminderTime.IsZero() {
		data.Children = append(data.Children, wbxml.Str(wbxml.TKReminderTime, t.ReminderTime.UTC().Format(easContactDate)))
	}
	if len(t.Categories) > 0 {
		var cats []*wbxml.Node
		for _, c := range t.Categories {
			cats = append(cats, wbxml.Str(wbxml.TKCategory, c))
		}
		data.Children = append(data.Children, wbxml.Elem(wbxml.TKCategories, cats...))
	}
	return data, nil
}

// parseTaskItem decodes a device's task ApplicationData into the shared model.
func parseTaskItem(data *wbxml.Node) oxtask.Task {
	t := oxtask.New()
	t.Subject = data.ChildText(wbxml.TKSubject)
	if body := data.Child(wbxml.ABBody); body != nil {
		if d := body.Child(wbxml.ABData); d != nil {
			if len(d.Opaque) > 0 {
				t.Body = string(d.Opaque)
			} else {
				t.Body = d.Text
			}
		}
	}
	if v := data.ChildText(wbxml.TKImportance); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			t.Importance = n
		}
	}
	if v := data.ChildText(wbxml.TKSensitivity); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			t.Sensitivity = n
		}
	}
	t.Start = taskDate(data, wbxml.TKUtcStartDate, wbxml.TKStartDate)
	t.Due = taskDate(data, wbxml.TKUtcDueDate, wbxml.TKDueDate)
	t.Complete = data.ChildText(wbxml.TKComplete) == "1"
	if d := data.ChildText(wbxml.TKDateCompleted); d != "" {
		if tm, err := time.Parse(easContactDate, d); err == nil {
			t.DateCompleted = tm.UTC()
		}
	}
	t.ReminderSet = data.ChildText(wbxml.TKReminderSet) == "1"
	if d := data.ChildText(wbxml.TKReminderTime); d != "" {
		if tm, err := time.Parse(easContactDate, d); err == nil {
			t.ReminderTime = tm.UTC()
		}
	}
	if cats := data.Child(wbxml.TKCategories); cats != nil {
		for _, c := range cats.Children {
			if c.Tag == wbxml.TKCategory && c.Text != "" {
				t.Categories = append(t.Categories, c.Text)
			}
		}
	}
	return t
}

// taskDate reads a task date, preferring the UTC field and falling back to the local.
func taskDate(data *wbxml.Node, utcTag, localTag wbxml.Tag) time.Time {
	for _, tag := range []wbxml.Tag{utcTag, localTag} {
		if s := data.ChildText(tag); s != "" {
			if tm, err := time.Parse(easContactDate, s); err == nil {
				return tm.UTC()
			}
		}
	}
	return time.Time{}
}

// applyTaskClientCommands applies a device's Add/Change/Delete commands to the Tasks
// folder, mirroring the contacts path through the shared task model.
func applyTaskClientCommands(st *objectstore.Store, cstate *collectionState, c *wbxml.Node) []*wbxml.Node {
	cmds := c.Child(wbxml.ASCommands)
	if cmds == nil {
		return nil
	}
	var responses []*wbxml.Node
	added := map[string]bool{}
	for _, cmd := range cmds.Children {
		switch cmd.Tag {
		case wbxml.ASAdd:
			clientID := cmd.ChildText(wbxml.ASClientID)
			data := cmd.Child(wbxml.ASData)
			if clientID == "" || data == nil {
				continue
			}
			props, err := oxtask.ToProps(parseTaskItem(data), st.GetNamedPropIDs)
			if err != nil {
				continue
			}
			id, err := st.CreateMessage(int64(mapi.PrivateFIDTasks), &oxcmail.Message{Props: props})
			if err != nil {
				continue
			}
			sid := strconv.FormatInt(id, 10)
			added[sid] = true
			responses = append(responses, wbxml.Elem(wbxml.ASAdd,
				wbxml.Str(wbxml.ASClientID, clientID),
				wbxml.Str(wbxml.ASServerID, sid),
				wbxml.Str(wbxml.ASStatus, strconv.Itoa(syncStatusOK))))
		case wbxml.ASChange:
			id, err := strconv.ParseInt(cmd.ChildText(wbxml.ASServerID), 10, 64)
			if err != nil {
				continue
			}
			data := cmd.Child(wbxml.ASData)
			if data == nil {
				continue
			}
			props, err := oxtask.ToProps(parseTaskItem(data), st.GetNamedPropIDs)
			if err != nil {
				continue
			}
			_ = st.SetMessageProperties(id, props)
		case wbxml.ASDelete:
			sid := cmd.ChildText(wbxml.ASServerID)
			id, err := strconv.ParseInt(sid, 10, 64)
			if err != nil {
				continue
			}
			if st.DeleteObject(id) == nil {
				delete(cstate.Items, sid)
			}
		}
	}
	if len(added) > 0 {
		if objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDTasks)); err == nil {
			for _, o := range objs {
				if sid := strconv.FormatInt(o.ID, 10); added[sid] {
					cstate.Items[sid] = int64(o.ChangeNumber)
				}
			}
		}
	}
	return responses
}
