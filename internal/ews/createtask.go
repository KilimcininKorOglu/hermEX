package ews

import (
	"encoding/xml"
	"errors"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
	"hermex/internal/oxtask"
)

// createTask is one <t:Task> of a CreateItem ([MS-OXWSTASK] TaskType), the fields
// the shared task model stores. Every other element lands in Other, and one that
// is not known to be safe to leave out refuses the item, so nothing the client set
// is dropped without a word.
type createTask struct {
	Subject string `xml:"Subject"`
	Body    struct {
		Type    string `xml:"BodyType,attr"`
		Content string `xml:",chardata"`
	} `xml:"Body"`
	Categories struct {
		String []string `xml:"String"`
	} `xml:"Categories"`
	Importance      string                   `xml:"Importance"`
	Sensitivity     string                   `xml:"Sensitivity"`
	ReminderDueBy   string                   `xml:"ReminderDueBy"`
	ReminderIsSet   bool                     `xml:"ReminderIsSet"`
	Extended        []oxews.ExtendedProperty `xml:"ExtendedProperty"`
	CompleteDate    string                   `xml:"CompleteDate"`
	DueDate         string                   `xml:"DueDate"`
	PercentComplete *float64                 `xml:"PercentComplete"`
	StartDate       string                   `xml:"StartDate"`
	Status          string                   `xml:"Status"`
	Other           []struct {
		XMLName xml.Name
	} `xml:",any"`
}

// ignorableTaskFields are the Task elements clients send that carry nothing this
// server stores apart from what it derives itself: the class is always IPM.Task,
// and IsComplete follows from Status.
var ignorableTaskFields = map[string]bool{"ItemClass": true, "IsComplete": true}

// taskStatusValues maps TaskStatusType ([MS-OXWSTASK] 2.2.5.5) to PidLidTaskStatus.
var taskStatusValues = map[string]int{
	"NotStarted": 0, "InProgress": 1, "Completed": 2, "WaitingOnOthers": 3, "Deferred": 4,
}

// errTaskField reports a Task element whose value this server cannot store.
var errTaskField = errors.New("ews: an unsupported task field")

// createTasks stores every task in the request, in the folder it names or Tasks.
func (s *Server) createTasks(st *objectstore.Store, req createItemRequest) []itemResponseMessage {
	if len(req.Items.Tasks) == 0 {
		return nil
	}
	fid, code := taskSaveFolder(st, req.SavedItemFolderID)
	msgs := make([]itemResponseMessage, 0, len(req.Items.Tasks))
	for _, item := range req.Items.Tasks {
		if code != "" {
			msgs = append(msgs, itemError(code))
			continue
		}
		msgs = append(msgs, createOneTask(st, fid, item))
	}
	return msgs
}

// taskSaveFolder is the folder a created task is stored in: the one the request
// names, which must be a task folder of the caller's own mailbox, else Tasks.
func taskSaveFolder(st *objectstore.Store, refs folderRefs) (int64, string) {
	targets := resolveTargets(refs)
	if len(targets) == 0 {
		return int64(mapi.PrivateFIDTasks), ""
	}
	t := targets[0]
	if !t.ok || t.mailbox != "" {
		return 0, "ErrorFolderNotFound"
	}
	if t.fid == int64(mapi.PrivateFIDTasks) {
		return t.fid, ""
	}
	props, err := st.GetFolderProperties(t.fid, mapi.PrContainerClass)
	if errors.Is(err, objectstore.ErrNotFound) {
		return 0, "ErrorFolderNotFound"
	}
	if err != nil {
		return 0, "ErrorInternalServerError"
	}
	class := strProp(props, mapi.PrContainerClass)
	if class != mapi.ContainerClassTask && !strings.HasPrefix(class, mapi.ContainerClassTask+".") {
		return 0, "ErrorInvalidRequest"
	}
	return t.fid, ""
}

// createOneTask stores one task through the shared task model every protocol
// writes tasks with, then the extended properties and HTML body the model has no
// field for.
func createOneTask(st *objectstore.Store, fid int64, item createTask) itemResponseMessage {
	for _, o := range item.Other {
		if !ignorableTaskFields[o.XMLName.Local] {
			return itemError("ErrorInvalidPropertySet")
		}
	}
	tk, err := item.model()
	if err != nil {
		return itemError("ErrorInvalidPropertySet")
	}
	extra, code := extendedValues(st, item.Extended)
	if code != "" {
		return itemError(code)
	}
	if strings.EqualFold(item.Body.Type, "HTML") && item.Body.Content != "" {
		extra.Set(mapi.PrHTML, []byte(oxews.ToCRLF(item.Body.Content)))
	}
	props, err := oxtask.ToProps(tk, st.GetNamedPropIDs)
	if err != nil {
		return itemError("ErrorInvalidPropertySet")
	}
	for _, pv := range extra {
		props.Set(pv.Tag, pv.Value)
	}
	id, err := st.CreateMessage(fid, &oxcmail.Message{Props: props})
	if err != nil {
		return itemError("ErrorItemSave")
	}
	itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: fid, MessageID: id})
	return itemFound(&itemsWrap{Tasks: []oxews.Task{{ItemID: oxews.ItemIDElem{ID: itemID, ChangeKey: changeKey(st, id)}}}})
}

// model reads the request's task into the shared task model.
func (item createTask) model() (oxtask.Task, error) {
	tk := oxtask.New()
	tk.Subject = item.Subject
	if !strings.EqualFold(item.Body.Type, "HTML") {
		tk.Body = item.Body.Content
	}
	tk.Categories = item.Categories.String
	tk.ReminderSet = item.ReminderIsSet
	if err := item.handling(&tk); err != nil {
		return tk, err
	}
	if err := item.dates(&tk); err != nil {
		return tk, err
	}
	return tk, item.progress(&tk)
}

// handling reads the importance and sensitivity names.
func (item createTask) handling(tk *oxtask.Task) error {
	if item.Importance != "" {
		v, ok := importanceValues[item.Importance]
		if !ok {
			return errTaskField
		}
		tk.Importance = int(v)
	}
	if item.Sensitivity != "" {
		v, ok := sensitivityValues[item.Sensitivity]
		if !ok {
			return errTaskField
		}
		tk.Sensitivity = int(v)
	}
	return nil
}

// dates reads the start, due, completion and reminder instants.
func (item createTask) dates(tk *oxtask.Task) error {
	for _, d := range []struct {
		raw string
		to  *time.Time
	}{
		{item.StartDate, &tk.Start},
		{item.DueDate, &tk.Due},
		{item.CompleteDate, &tk.DateCompleted},
		{item.ReminderDueBy, &tk.ReminderTime},
	} {
		if d.raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(d.raw))
		if err != nil {
			return errTaskField
		}
		*d.to = t.UTC()
	}
	return nil
}

// progress reads the status and the percent complete, which EWS counts from 0 to
// 100 and the task model from 0 to 1. A task is complete when its status says so.
func (item createTask) progress(tk *oxtask.Task) error {
	if item.Status != "" {
		v, ok := taskStatusValues[item.Status]
		if !ok {
			return errTaskField
		}
		tk.Status = v
		tk.Complete = v == taskStatusValues["Completed"]
	}
	if p := item.PercentComplete; p != nil {
		if *p < 0 || *p > 100 {
			return errTaskField
		}
		tk.PercentComplete = *p / 100
	}
	return nil
}
