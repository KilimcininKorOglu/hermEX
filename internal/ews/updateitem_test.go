package ews

import (
	"regexp"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

var folderIDRE = regexp.MustCompile(`<(?:\w+:)?FolderId Id="([^"]+)"`)

// TestUpdateItemMarkRead confirms a message:IsRead update sets the Seen flag.
func TestUpdateItemMarkRead(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	m := itemIDRE.FindStringSubmatch(fi)
	if len(m) != 2 {
		t.Fatalf("no ItemId: %s", fi)
	}
	req := wrapRequest(`<UpdateItem ConflictResolution="AutoResolve" xmlns="` + nsMessages + `">` +
		`<ItemChanges><t:ItemChange xmlns:t="` + nsTypes + `">` +
		`<t:ItemId Id="` + m[1] + `"/>` +
		`<t:Updates><t:SetItemField><t:FieldURI FieldURI="message:IsRead"/>` +
		`<t:Message><t:IsRead>true</t:IsRead></t:Message></t:SetItemField></t:Updates>` +
		`</t:ItemChange></ItemChanges></UpdateItem>`)
	_, out := soapPost(t, ts, req, true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateItem not success: %s", out)
	}
	st, msgs := inboxUIDs(t, dir)
	defer st.Close()
	flags, _ := st.MessageFlags(int64(mapi.PrivateFIDInbox), msgs[0].UID)
	if flags&objectstore.FlagSeen == 0 {
		t.Errorf("IsRead=true did not set the Seen flag")
	}
}

// TestDeleteItemHard confirms HardDelete removes the message.
func TestDeleteItemHard(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	itemID := itemIDRE.FindStringSubmatch(fi)[1]
	req := wrapRequest(`<DeleteItem DeleteType="HardDelete" xmlns="` + nsMessages + `">` +
		`<ItemIds><t:ItemId Id="` + itemID + `" xmlns:t="` + nsTypes + `"/></ItemIds></DeleteItem>`)
	_, out := soapPost(t, ts, req, true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("DeleteItem not success: %s", out)
	}
	st, msgs := inboxUIDs(t, dir)
	st.Close()
	if len(msgs) != 0 {
		t.Errorf("inbox = %d, want 0 after hard delete", len(msgs))
	}
}

// TestDeleteItemSoft confirms MoveToDeletedItems moves the message to Deleted
// Items rather than removing it.
func TestDeleteItemSoft(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	itemID := itemIDRE.FindStringSubmatch(fi)[1]
	req := wrapRequest(`<DeleteItem DeleteType="MoveToDeletedItems" xmlns="` + nsMessages + `">` +
		`<ItemIds><t:ItemId Id="` + itemID + `" xmlns:t="` + nsTypes + `"/></ItemIds></DeleteItem>`)
	soapPost(t, ts, req, true)

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	inbox, _ := st.ListMessages(int64(mapi.PrivateFIDInbox))
	deleted, _ := st.ListMessages(int64(mapi.PrivateFIDDeletedItems))
	if len(inbox) != 0 {
		t.Errorf("inbox = %d, want 0 after soft delete", len(inbox))
	}
	if len(deleted) != 1 {
		t.Errorf("deleted items = %d, want 1", len(deleted))
	}
}

// TestMoveItem confirms MoveItem relocates a message to the target folder.
func TestMoveItem(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	itemID := itemIDRE.FindStringSubmatch(fi)[1]
	req := wrapRequest(`<MoveItem xmlns="` + nsMessages + `">` +
		`<ToFolderId><t:DistinguishedFolderId Id="drafts" xmlns:t="` + nsTypes + `"/></ToFolderId>` +
		`<ItemIds><t:ItemId Id="` + itemID + `" xmlns:t="` + nsTypes + `"/></ItemIds></MoveItem>`)
	_, out := soapPost(t, ts, req, true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("MoveItem not success: %s", out)
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	inbox, _ := st.ListMessages(int64(mapi.PrivateFIDInbox))
	drafts, _ := st.ListMessages(int64(mapi.PrivateFIDDraft))
	if len(inbox) != 0 || len(drafts) != 1 {
		t.Errorf("after move: inbox=%d drafts=%d, want 0 and 1", len(inbox), len(drafts))
	}
}

// TestCreateAndDeleteFolder confirms a user folder can be created and deleted.
func TestCreateAndDeleteFolder(t *testing.T) {
	ts, _ := seededWithMessage(t)
	cf := wrapRequest(`<CreateFolder xmlns="` + nsMessages + `">` +
		`<ParentFolderId><t:DistinguishedFolderId Id="msgfolderroot" xmlns:t="` + nsTypes + `"/></ParentFolderId>` +
		`<Folders><t:Folder xmlns:t="` + nsTypes + `"><t:DisplayName>Project X</t:DisplayName></t:Folder></Folders></CreateFolder>`)
	_, out := soapPost(t, ts, cf, true)
	if !strings.Contains(out, `ResponseClass="Success"`) || !strings.Contains(out, "Project X") {
		t.Fatalf("CreateFolder failed: %s", out)
	}
	m := folderIDRE.FindStringSubmatch(out)
	if len(m) != 2 {
		t.Fatalf("CreateFolder returned no FolderId: %s", out)
	}
	df := wrapRequest(`<DeleteFolder DeleteType="HardDelete" xmlns="` + nsMessages + `">` +
		`<FolderIds><t:FolderId Id="` + m[1] + `" xmlns:t="` + nsTypes + `"/></FolderIds></DeleteFolder>`)
	_, dout := soapPost(t, ts, df, true)
	if !strings.Contains(dout, `ResponseClass="Success"`) {
		t.Errorf("DeleteFolder failed: %s", dout)
	}
}

// TestDeleteBuiltinFolderRejected confirms a built-in folder cannot be deleted.
func TestDeleteBuiltinFolderRejected(t *testing.T) {
	ts, _ := seededWithMessage(t)
	inboxID := oxews.EncodeFolderID(int64(mapi.PrivateFIDInbox))
	df := wrapRequest(`<DeleteFolder DeleteType="HardDelete" xmlns="` + nsMessages + `">` +
		`<FolderIds><t:FolderId Id="` + inboxID + `" xmlns:t="` + nsTypes + `"/></FolderIds></DeleteFolder>`)
	_, out := soapPost(t, ts, df, true)
	if !strings.Contains(out, "ErrorDeleteDistinguishedFolder") {
		t.Errorf("built-in folder delete should be rejected: %s", out)
	}
}
