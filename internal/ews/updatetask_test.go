package ews

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
	"hermex/internal/oxtask"
)

// setTaskField is a SetItemField carrying value in a <t:Task>.
func setTaskField(uri, value string) string {
	return `<t:SetItemField><t:FieldURI FieldURI="` + uri + `"/><t:Task>` + value + `</t:Task></t:SetItemField>`
}

// updateTaskReq is an UpdateItem of one task with the given updates.
func updateTaskReq(itemID, fields string) string {
	return wrapRequest(`<UpdateItem xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" ConflictResolution="AutoResolve">` +
		`<ItemChanges><t:ItemChange><t:ItemId Id="` + itemID + `"/><t:Updates>` + fields + `</t:Updates></t:ItemChange></ItemChanges></UpdateItem>`)
}

// storedTask seeds one task in Tasks, with a property the task model does not
// cover, and returns its id.
func storedTask(t *testing.T, dir string) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	tk := oxtask.New()
	tk.Subject = "Draft the plan"
	tk.Due = time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	tk.Sensitivity = 2
	props, err := oxtask.ToProps(tk, st.GetNamedPropIDs)
	mustNoErr(t, "render the task", err)
	props.Set(mapi.PrMessageFlags, int32(1))
	id, err := st.CreateMessage(int64(mapi.PrivateFIDTasks), &oxcmail.Message{Props: props})
	mustNoErr(t, "store the task", err)
	return id
}

// TestUpdateItemEditsATaskInPlace proves UpdateItem edits a task through the
// shared task model and keeps it the same object: the named fields change, a
// deleted field leaves the store, and what the request does not name stays.
// UpdateItem sent a task to the calendar edit, which refused it, so an EWS
// client could not change or complete a task.
func TestUpdateItemEditsATaskInPlace(t *testing.T) {
	ts, dir := seededWithMessage(t)
	id := storedTask(t, dir)
	itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDTasks), MessageID: id})

	_, out := soapPost(t, ts, updateTaskReq(itemID,
		setTaskField("item:Subject", `<t:Subject>Ship the plan</t:Subject>`)+
			setTaskField("task:Status", `<t:Status>Completed</t:Status>`)+
			setTaskField("task:CompleteDate", `<t:CompleteDate>2026-10-08T12:00:00Z</t:CompleteDate>`)+
			`<t:DeleteItemField><t:FieldURI FieldURI="task:DueDate"/></t:DeleteItemField>`), true)
	wantContains(t, "the update", out, `ResponseClass="Success"`)
	wantContains(t, "the updated task id", out, itemID)

	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	msg, err := st.OpenMessage(id)
	mustNoErr(t, "the task keeps its id", err)
	tk, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
	mustNoErr(t, "read the task", err)
	wantEq(t, "the subject", tk.Subject, "Ship the plan")
	wantEq(t, "the complete flag", tk.Complete, true)
	wantEq(t, "the status", tk.Status, 2)
	wantEq(t, "the completion date", tk.DateCompleted, time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	wantEq(t, "the cleared due date", tk.Due.IsZero(), true)
	wantEq(t, "the untouched sensitivity", tk.Sensitivity, 2)
	flags, _ := msg.Props.Get(mapi.PrMessageFlags)
	wantEq[any](t, "the property the model does not cover", flags, int32(1))
}

// TestUpdateItemRefusesATaskFieldItCannotWrite proves a task field UpdateItem
// cannot write refuses the change and leaves the task as it was.
func TestUpdateItemRefusesATaskFieldItCannotWrite(t *testing.T) {
	ts, dir := seededWithMessage(t)
	id := storedTask(t, dir)
	itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDTasks), MessageID: id})
	_, out := soapPost(t, ts, updateTaskReq(itemID,
		setTaskField("item:Subject", `<t:Subject>Changed</t:Subject>`)+setTaskField("task:Mileage", `<t:Mileage>9</t:Mileage>`)), true)
	wantContains(t, "the refusal", out, "ErrorInvalidPropertySet")

	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	msg, err := st.OpenMessage(id)
	mustNoErr(t, "open the task", err)
	tk, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
	mustNoErr(t, "read the task", err)
	wantEq(t, "the subject", tk.Subject, "Draft the plan")
}
