package ews

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxtask"
)

// createTaskReq is a CreateItem of one Task with the given item body.
func createTaskReq(item string) string {
	return wrapRequest(`<CreateItem xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" MessageDisposition="SaveOnly">` +
		`<Items><t:Task>` + item + `</t:Task></Items></CreateItem>`)
}

// TestCreateItemStoresATask proves a Task created over EWS is the one task every
// protocol reads: it lands in Tasks through the shared task model with its dates,
// progress and handling, and GetItem serves it back. CreateItem dropped the Task
// element and answered with nothing, so an EWS client could not add a task.
func TestCreateItemStoresATask(t *testing.T) {
	ts, dir := seededWithMessage(t)
	_, out := soapPost(t, ts, createTaskReq(`<t:Subject>File the report</t:Subject>`+
		`<t:Body BodyType="Text">quarterly numbers</t:Body>`+
		`<t:Categories><t:String>Work</t:String></t:Categories>`+
		`<t:Importance>High</t:Importance>`+
		`<t:ReminderDueBy>2026-10-09T08:00:00Z</t:ReminderDueBy><t:ReminderIsSet>true</t:ReminderIsSet>`+
		`<t:DueDate>2026-10-10T00:00:00Z</t:DueDate><t:PercentComplete>50</t:PercentComplete>`+
		`<t:StartDate>2026-10-01T00:00:00Z</t:StartDate><t:Status>InProgress</t:Status>`), true)
	wantContains(t, "the create", out, `ResponseClass="Success"`)
	wantContains(t, "the created item", out, "<Task")

	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDTasks))
	mustNoErr(t, "list Tasks", err)
	if len(objs) != 1 {
		t.Fatalf("Tasks holds %d items, want the created task", len(objs))
	}
	msg, err := st.OpenMessage(objs[0].ID)
	mustNoErr(t, "open the task", err)
	tk, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
	mustNoErr(t, "read the task", err)
	st.Close()

	wantEq(t, "the subject", tk.Subject, "File the report")
	wantEq(t, "the body", tk.Body, "quarterly numbers")
	wantEq(t, "the importance", tk.Importance, 2)
	wantEq(t, "the status", tk.Status, 1)
	wantEq(t, "the percent complete", tk.PercentComplete, 0.5)
	wantEq(t, "the due date", tk.Due, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
	wantEq(t, "the start date", tk.Start, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	wantEq(t, "the reminder", tk.ReminderTime, time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC))
	wantEq(t, "the reminder flag", tk.ReminderSet, true)
	wantEq(t, "the complete flag", tk.Complete, false)
	if len(tk.Categories) != 1 || tk.Categories[0] != "Work" {
		t.Errorf("categories = %v, want [Work]", tk.Categories)
	}

	id := itemIDRE.FindStringSubmatch(out)
	if len(id) != 2 {
		t.Fatalf("CreateItem returned no ItemId: %s", out)
	}
	_, got := soapPost(t, ts, getItemReq(id[1]), true)
	wantContains(t, "the GetItem subject", got, "File the report")
	wantContains(t, "the GetItem due date", got, "2026-10-10T00:00:00Z")
}

// TestCreateItemRefusesATaskFieldItCannotStore proves a Task element the server
// would drop refuses the item instead of storing a task without it.
func TestCreateItemRefusesATaskFieldItCannotStore(t *testing.T) {
	ts, dir := seededWithMessage(t)
	for name, item := range map[string]string{
		"unknown element": `<t:Subject>x</t:Subject><t:Mileage>12</t:Mileage>`,
		"unknown status":  `<t:Subject>x</t:Subject><t:Status>Someday</t:Status>`,
		"bad due date":    `<t:Subject>x</t:Subject><t:DueDate>tomorrow</t:DueDate>`,
	} {
		_, out := soapPost(t, ts, createTaskReq(item), true)
		wantContains(t, name, out, "ErrorInvalidPropertySet")
	}
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDTasks))
	mustNoErr(t, "list Tasks", err)
	wantEq(t, "the stored tasks", len(objs), 0)
}
