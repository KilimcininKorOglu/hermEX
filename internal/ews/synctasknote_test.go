package ews

import (
	"net/http"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxtask"
)

// syncObjectFolder drives a create, an edit and a removal of one stored object
// through SyncFolderItems on a distinguished folder, and checks each is reported
// in the element the folder's items render as.
func syncObjectFolder(t *testing.T, folder string, fid int64, props mapi.PropertyValues, element string) {
	t.Helper()
	ts, dir := seededWithMessage(t)
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	id, err := st.CreateMessage(fid, &oxcmail.Message{Props: props})
	mustNoErr(t, "store the item", err)
	mustCloseStore(t, st)

	resp, out := soapPost(t, ts, syncItemsReq(folder, "", 0), true)
	wantEq(t, "prime sync status", resp.StatusCode, http.StatusOK)
	if countChange(out, "Create") != 1 {
		t.Fatalf("the prime reports %d creates, want the item: %s", countChange(out, "Create"), out)
	}
	wantContains(t, "the created item", out, "<"+element)
	wantContains(t, "the created subject", out, "<Subject>Synced</Subject>")
	state := syncStateRE.FindStringSubmatch(out)[1]

	resp, badOut := soapPost(t, ts, syncItemsReq(folder, "forged-sync-state", 0), true)
	wantEq(t, "forged sync status", resp.StatusCode, http.StatusOK)
	wantContains(t, "forged sync error", badOut, "ErrorInvalidSyncStateData")

	st, err = objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	mustNoErr(t, "edit the item", st.SetMessageProperties(id, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "Edited"}}))
	mustCloseStore(t, st)

	resp, out = soapPost(t, ts, syncItemsReq(folder, state, 0), true)
	wantEq(t, "update sync status", resp.StatusCode, http.StatusOK)
	if countChange(out, "Update") != 1 || countChange(out, "Create") != 0 {
		t.Fatalf("the edit reports creates=%d updates=%d, want one update: %s", countChange(out, "Create"), countChange(out, "Update"), out)
	}
	wantContains(t, "the updated item", out, "<"+element)
	wantContains(t, "the updated subject", out, "<Subject>Edited</Subject>")
	state = syncStateRE.FindStringSubmatch(out)[1]

	st, err = objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	mustNoErr(t, "delete the item", st.DeleteObject(id))
	mustCloseStore(t, st)

	resp, out = soapPost(t, ts, syncItemsReq(folder, state, 0), true)
	wantEq(t, "delete sync status", resp.StatusCode, http.StatusOK)
	if countChange(out, "Delete") != 1 {
		t.Fatalf("the removal reports %d deletes, want 1: %s", countChange(out, "Delete"), out)
	}
}

// TestSyncFolderItemsReportsTaskChanges proves a Tasks folder synced over EWS
// reports a task as a Task when it is created, edited and removed. The folder was
// diffed against the IMAP index, where tasks never are, so a syncing client saw
// no task at all.
func TestSyncFolderItemsReportsTaskChanges(t *testing.T) {
	syncObjectFolder(t, "tasks", int64(mapi.PrivateFIDTasks), mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: oxtask.MessageClass},
		{Tag: mapi.PrSubject, Value: "Synced"},
	}, "Task")
}

// TestAUserTaskFolderHoldsTasks proves a task folder a client created is read
// like Tasks: a task created into it is stored there, and FindItem and
// SyncFolderItems report it as a Task. Only the built-in Tasks folder was read
// from the object store, so FindItem listed a user task folder as empty mail.
func TestAUserTaskFolderHoldsTasks(t *testing.T) {
	ts, _ := seededEWS(t)
	resp, out := soapPost(t, ts, wrapRequest(`<CreateFolder xmlns="`+nsMessages+`" xmlns:t="`+nsTypes+`">`+
		`<ParentFolderId><t:DistinguishedFolderId Id="msgfolderroot"/></ParentFolderId>`+
		`<Folders><t:TasksFolder><t:DisplayName>Chores</t:DisplayName></t:TasksFolder></Folders></CreateFolder>`), true)
	wantEq(t, "CreateFolder task status", resp.StatusCode, http.StatusOK)
	folder := createdIDRE.FindStringSubmatch(out)
	if len(folder) != 2 {
		t.Fatalf("CreateFolder = %s, want a folder id", out)
	}
	resp, out = soapPost(t, ts, wrapRequest(`<CreateItem xmlns="`+nsMessages+`" xmlns:t="`+nsTypes+`" MessageDisposition="SaveOnly">`+
		`<SavedItemFolderId><t:FolderId Id="`+folder[1]+`"/></SavedItemFolderId>`+
		`<Items><t:Task><t:Subject>Sweep</t:Subject></t:Task></Items></CreateItem>`), true)
	wantEq(t, "CreateItem user task status", resp.StatusCode, http.StatusOK)
	wantContains(t, "the create", out, `ResponseClass="Success"`)

	resp, out = soapPost(t, ts, wrapRequest(`<FindItem xmlns="`+nsMessages+`" xmlns:t="`+nsTypes+`" Traversal="Shallow">`+
		`<ItemShape><t:BaseShape>Default</t:BaseShape></ItemShape>`+
		`<ParentFolderIds><t:FolderId Id="`+folder[1]+`"/></ParentFolderIds></FindItem>`), true)
	wantEq(t, "FindItem user task status", resp.StatusCode, http.StatusOK)
	wantContains(t, "the FindItem task", out, "<Task")
	wantContains(t, "the FindItem subject", out, "<Subject>Sweep</Subject>")

	resp, out = soapPost(t, ts, wrapRequest(`<SyncFolderItems xmlns="`+nsMessages+`" xmlns:t="`+nsTypes+`">`+
		`<ItemShape><t:BaseShape>Default</t:BaseShape></ItemShape>`+
		`<SyncFolderId><t:FolderId Id="`+folder[1]+`"/></SyncFolderId><MaxChangesReturned>10</MaxChangesReturned></SyncFolderItems>`), true)
	wantEq(t, "SyncFolderItems user task status", resp.StatusCode, http.StatusOK)
	wantEq(t, "the synced creates", countChange(out, "Create"), 1)
	wantContains(t, "the synced task", out, "<Task")
}

// TestSyncFolderItemsReportsNoteChanges proves the same for a Notes folder, whose
// notes are reported as base Items.
func TestSyncFolderItemsReportsNoteChanges(t *testing.T) {
	syncObjectFolder(t, "notes", int64(mapi.PrivateFIDNotes), mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.StickyNote"},
		{Tag: mapi.PrSubject, Value: "Synced"},
	}, "Item")
}
