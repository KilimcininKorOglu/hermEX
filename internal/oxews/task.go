package oxews

import (
	"encoding/xml"

	"hermex/internal/oxtask"
)

// Task is the EWS <t:Task> element (MS-OXWSTASK), the subset v1 emits, with its
// children in the Types.xsd sequence order.
type Task struct {
	XMLName         xml.Name    `xml:"http://schemas.microsoft.com/exchange/services/2006/types Task"`
	ItemID          ItemIDElem  `xml:"ItemId"`
	Subject         string      `xml:"Subject,omitempty"`
	Sensitivity     string      `xml:"Sensitivity,omitempty"`
	Body            *Body       `xml:"Body,omitempty"`
	Categories      *Categories `xml:"Categories,omitempty"`
	Importance      string      `xml:"Importance,omitempty"`
	ReminderDueBy   string      `xml:"ReminderDueBy,omitempty"`
	ReminderIsSet   bool        `xml:"ReminderIsSet"`
	HasAttachments  bool        `xml:"HasAttachments"`
	CompleteDate    string      `xml:"CompleteDate,omitempty"`
	DueDate         string      `xml:"DueDate,omitempty"`
	IsComplete      bool        `xml:"IsComplete"`
	PercentComplete float64     `xml:"PercentComplete"`
	StartDate       string      `xml:"StartDate,omitempty"`
	Status          string      `xml:"Status,omitempty"`
}

// Categories is the EWS <t:Categories> element: a list of <t:String> values.
type Categories struct {
	String []string `xml:"http://schemas.microsoft.com/exchange/services/2006/types String"`
}

// BuildTask renders a <t:Task> from the shared task model plus the item metadata.
// The body, dates, and categories come from the task; the id and attachment flag
// from the metadata.
func BuildTask(t oxtask.Task, meta ItemMeta) Task {
	out := Task{
		ItemID:         ItemIDElem{ID: meta.ItemID, ChangeKey: meta.ChangeKey},
		Subject:        t.Subject,
		HasAttachments: meta.HasAttachments,
		IsComplete:     t.Complete,
		ReminderIsSet:  t.ReminderSet,
	}
	if t.Body != "" {
		out.Body = &Body{BodyType: "Text", Content: t.Body}
	}
	if t.Importance >= 0 {
		out.Importance = importanceName(int32(t.Importance))
	}
	if t.Sensitivity >= 0 {
		out.Sensitivity = sensitivityName(int32(t.Sensitivity))
	}
	if len(t.Categories) > 0 {
		out.Categories = &Categories{String: t.Categories}
	}
	if !t.Start.IsZero() {
		out.StartDate = t.Start.UTC().Format(ewsTime)
	}
	if !t.Due.IsZero() {
		out.DueDate = t.Due.UTC().Format(ewsTime)
	}
	if t.ReminderSet && !t.ReminderTime.IsZero() {
		out.ReminderDueBy = t.ReminderTime.UTC().Format(ewsTime)
	}
	if t.Complete {
		out.PercentComplete = 100
		out.Status = "Completed"
		if !t.DateCompleted.IsZero() {
			out.CompleteDate = t.DateCompleted.UTC().Format(ewsTime)
		}
	} else {
		out.Status = "NotStarted"
	}
	return out
}
