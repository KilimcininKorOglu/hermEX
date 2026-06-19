package ews

import (
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// moveCopyReq builds a MoveFolder or CopyFolder request moving/copying one folder
// (by id) into a destination folder (by id).
func moveCopyReq(op, toID, folderID string) string {
	return wrapRequest(`<` + op + ` xmlns="` + nsMessages + `">` +
		`<ToFolderId><t:FolderId Id="` + toID + `" xmlns:t="` + nsTypes + `"/></ToFolderId>` +
		`<FolderIds><t:FolderId Id="` + folderID + `" xmlns:t="` + nsTypes + `"/></FolderIds>` +
		`</` + op + `>`)
}

// findFolderByIDReq lists a folder's children by the parent's (non-distinguished)
// FolderId.
func findFolderByIDReq(parentID, traversal string) string {
	return wrapRequest(`<FindFolder Traversal="` + traversal + `" xmlns="` + nsMessages + `">` +
		`<FolderShape><BaseShape>Default</BaseShape></FolderShape>` +
		`<ParentFolderIds><t:FolderId Id="` + parentID + `" xmlns:t="` + nsTypes + `"/></ParentFolderIds>` +
		`</FindFolder>`)
}

// createFolderReq builds a CreateFolder request adding one named folder under a
// distinguished parent.
func createFolderReq(parent, name string) string {
	return wrapRequest(`<CreateFolder xmlns="` + nsMessages + `">` +
		`<ParentFolderId><t:DistinguishedFolderId Id="` + parent + `" xmlns:t="` + nsTypes + `"/></ParentFolderId>` +
		`<Folders><t:Folder xmlns:t="` + nsTypes + `"><t:DisplayName>` + name + `</t:DisplayName></t:Folder></Folders>` +
		`</CreateFolder>`)
}

// createUserFolder creates a folder under the distinguished parent and returns
// its FolderId.
func createUserFolder(t *testing.T, ts *httptest.Server, parent, name string) string {
	t.Helper()
	_, out := soapPost(t, ts, createFolderReq(parent, name), true)
	m := folderIDRE.FindStringSubmatch(out)
	if len(m) != 2 {
		t.Fatalf("CreateFolder(%q) returned no FolderId: %s", name, out)
	}
	return m[1]
}

// updateFolderRenameReq builds an UpdateFolder request renaming a folder (by id)
// via a folder:DisplayName SetFolderField.
func updateFolderRenameReq(folderID, newName string) string {
	return wrapRequest(`<UpdateFolder xmlns="` + nsMessages + `">` +
		`<FolderChanges><t:FolderChange xmlns:t="` + nsTypes + `">` +
		`<t:FolderId Id="` + folderID + `"/>` +
		`<t:Updates><t:SetFolderField>` +
		`<t:FieldURI FieldURI="folder:DisplayName"/>` +
		`<t:Folder><t:DisplayName>` + newName + `</t:DisplayName></t:Folder>` +
		`</t:SetFolderField></t:Updates>` +
		`</t:FolderChange></FolderChanges></UpdateFolder>`)
}

// TestUpdateFolderRename confirms a folder:DisplayName update renames a user
// folder — the new name appears in a follow-up FindFolder, the old one does not.
func TestUpdateFolderRename(t *testing.T) {
	ts, _ := seededEWS(t)
	id := createUserFolder(t, ts, "inbox", "Old")

	resp, out := soapPost(t, ts, updateFolderRenameReq(id, "New"), true)
	if resp.StatusCode != 200 {
		t.Fatalf("UpdateFolder status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateFolder not success: %s", out)
	}

	_, listing := soapPost(t, ts, findFolderReq("inbox", "Shallow"), true)
	if !strings.Contains(listing, "New") {
		t.Errorf("renamed folder absent from FindFolder: %s", listing)
	}
	if strings.Contains(listing, "Old") {
		t.Errorf("old folder name still present after rename: %s", listing)
	}
}

// TestUpdateFolderDistinguishedRejected confirms renaming a well-known folder
// (whose name is fixed) is refused.
func TestUpdateFolderDistinguishedRejected(t *testing.T) {
	ts, _ := seededEWS(t)
	req := wrapRequest(`<UpdateFolder xmlns="` + nsMessages + `">` +
		`<FolderChanges><t:FolderChange xmlns:t="` + nsTypes + `">` +
		`<t:DistinguishedFolderId Id="inbox"/>` +
		`<t:Updates><t:SetFolderField>` +
		`<t:FieldURI FieldURI="folder:DisplayName"/>` +
		`<t:Folder><t:DisplayName>Hacked</t:DisplayName></t:Folder>` +
		`</t:SetFolderField></t:Updates>` +
		`</t:FolderChange></FolderChanges></UpdateFolder>`)
	if _, out := soapPost(t, ts, req, true); !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("renaming a distinguished folder must be ErrorAccessDenied: %s", out)
	}
}

// TestUpdateFolderNameCollision confirms renaming a folder to a name a sibling
// already holds is refused with ErrorFolderExists.
func TestUpdateFolderNameCollision(t *testing.T) {
	ts, _ := seededEWS(t)
	createUserFolder(t, ts, "inbox", "Alpha")
	beta := createUserFolder(t, ts, "inbox", "Beta")

	if _, out := soapPost(t, ts, updateFolderRenameReq(beta, "Alpha"), true); !strings.Contains(out, "ErrorFolderExists") {
		t.Errorf("renaming onto a sibling's name must be ErrorFolderExists: %s", out)
	}
}

// TestMoveFolder confirms a folder is reparented under the destination, keeps its
// id, and leaves its original parent.
func TestMoveFolder(t *testing.T) {
	ts, _ := seededEWS(t)
	box := createUserFolder(t, ts, "inbox", "Box")
	dest := createUserFolder(t, ts, "inbox", "Dest")

	resp, out := soapPost(t, ts, moveCopyReq("MoveFolder", dest, box), true)
	if resp.StatusCode != 200 || !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("MoveFolder not success (%d): %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, box) {
		t.Errorf("a move must return the same folder id: %s", out)
	}
	if _, listing := soapPost(t, ts, findFolderByIDReq(dest, "Shallow"), true); !strings.Contains(listing, "Box") {
		t.Errorf("moved folder absent under the destination: %s", listing)
	}
	if _, listing := soapPost(t, ts, findFolderReq("inbox", "Shallow"), true); strings.Contains(listing, "Box") {
		t.Errorf("moved folder still present under the original parent: %s", listing)
	}
}

// TestMoveFolderDistinguishedRejected confirms a well-known folder cannot be moved
// (reparenting it would corrupt the well-known hierarchy).
func TestMoveFolderDistinguishedRejected(t *testing.T) {
	ts, _ := seededEWS(t)
	dest := createUserFolder(t, ts, "inbox", "Dest")
	req := wrapRequest(`<MoveFolder xmlns="` + nsMessages + `">` +
		`<ToFolderId><t:FolderId Id="` + dest + `" xmlns:t="` + nsTypes + `"/></ToFolderId>` +
		`<FolderIds><t:DistinguishedFolderId Id="inbox" xmlns:t="` + nsTypes + `"/></FolderIds>` +
		`</MoveFolder>`)
	if _, out := soapPost(t, ts, req, true); !strings.Contains(out, "ErrorMoveDistinguishedFolder") {
		t.Errorf("moving a distinguished folder must be ErrorMoveDistinguishedFolder: %s", out)
	}
}

// TestMoveFolderCollision confirms moving a folder under a destination that
// already holds one of the same name is refused.
func TestMoveFolderCollision(t *testing.T) {
	ts, dir := seededEWS(t)
	dest := createUserFolder(t, ts, "inbox", "Dest")
	box := createUserFolder(t, ts, "inbox", "Box")
	// Seed a same-named folder already under the destination.
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	destFID, _ := oxews.DecodeFolderID(dest)
	if _, err := st.CreateFolder(&destFID, "Box"); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()

	if _, out := soapPost(t, ts, moveCopyReq("MoveFolder", dest, box), true); !strings.Contains(out, "ErrorFolderExists") {
		t.Errorf("moving onto a destination name collision must be ErrorFolderExists: %s", out)
	}
}

// TestMoveFolderCycle confirms moving a folder into its own descendant is refused
// (the cycle the store guard catches, surfaced as ErrorMoveCopyFailed).
func TestMoveFolderCycle(t *testing.T) {
	ts, dir := seededEWS(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	inbox := int64(mapi.PrivateFIDInbox)
	parent, err := st.CreateFolder(&inbox, "P")
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	child, err := st.CreateFolder(&parent, "C")
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()

	req := moveCopyReq("MoveFolder", oxews.EncodeFolderID(child), oxews.EncodeFolderID(parent))
	if _, out := soapPost(t, ts, req, true); !strings.Contains(out, "ErrorMoveCopyFailed") {
		t.Errorf("moving a folder into its descendant must be ErrorMoveCopyFailed: %s", out)
	}
}

// TestCopyFolder confirms a copy lands under the destination with a new id,
// leaving the source in place.
func TestCopyFolder(t *testing.T) {
	ts, _ := seededEWS(t)
	src := createUserFolder(t, ts, "inbox", "Src")
	dest := createUserFolder(t, ts, "inbox", "Dst")

	resp, out := soapPost(t, ts, moveCopyReq("CopyFolder", dest, src), true)
	if resp.StatusCode != 200 || !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("CopyFolder not success (%d): %s", resp.StatusCode, out)
	}
	m := folderIDRE.FindStringSubmatch(out)
	if len(m) != 2 {
		t.Fatalf("CopyFolder returned no FolderId: %s", out)
	}
	if m[1] == src {
		t.Errorf("a copy must get a new folder id, got the source's id: %s", out)
	}
	if _, listing := soapPost(t, ts, findFolderByIDReq(dest, "Shallow"), true); !strings.Contains(listing, "Src") {
		t.Errorf("copied folder absent under the destination: %s", listing)
	}
	// The source remains where it was.
	if _, listing := soapPost(t, ts, findFolderReq("inbox", "Shallow"), true); !strings.Contains(listing, "Src") {
		t.Errorf("source folder must remain after a copy: %s", listing)
	}
}

// permissionXML builds a single <t:Permission> with the given UserId inner XML and
// canned PermissionLevel.
func permissionXML(userID, level string) string {
	return `<t:Permission><t:UserId>` + userID + `</t:UserId>` +
		`<t:PermissionLevel>` + level + `</t:PermissionLevel></t:Permission>`
}

// updatePermsReq builds an UpdateFolder request setting folder:PermissionSet on a
// folder (addressed by the given <t:FolderId>/<t:DistinguishedFolderId> element) to
// the given permission members.
func updatePermsReq(folderRef, permissions string) string {
	return wrapRequest(`<UpdateFolder xmlns="` + nsMessages + `">` +
		`<FolderChanges><t:FolderChange xmlns:t="` + nsTypes + `">` +
		folderRef +
		`<t:Updates><t:SetFolderField>` +
		`<t:FieldURI FieldURI="folder:PermissionSet"/>` +
		`<t:Folder><t:PermissionSet><t:Permissions>` + permissions + `</t:Permissions></t:PermissionSet></t:Folder>` +
		`</t:SetFolderField></t:Updates>` +
		`</t:FolderChange></FolderChanges></UpdateFolder>`)
}

// folderPerms reads a folder's stored permission rows keyed by member name (the
// special members are "default"/"anonymous", a real member its SMTP address).
func folderPerms(t *testing.T, dir string, fid int64) map[string]uint32 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	entries, err := st.ListPermissions(fid)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]uint32{}
	for _, e := range entries {
		m[e.Name] = e.Rights
	}
	return m
}

// TestUpdateFolderPermissionSetReplaces confirms a folder:PermissionSet write
// stores each member's rights and that a second write fully replaces the ACL (a
// member absent from the second set is dropped — MS-OXWSFOLD replace semantics).
func TestUpdateFolderPermissionSetReplaces(t *testing.T) {
	ts, dir := seededEWS(t)
	box := createUserFolder(t, ts, "inbox", "Shared")
	fid, err := oxews.DecodeFolderID(box)
	if err != nil {
		t.Fatal(err)
	}

	perms := permissionXML(`<t:DistinguishedUser>Default</t:DistinguishedUser>`, "Reviewer") +
		permissionXML(`<t:PrimarySmtpAddress>alice@hermex.test</t:PrimarySmtpAddress>`, "Author")
	resp, out := soapPost(t, ts, updatePermsReq(`<t:FolderId Id="`+box+`"/>`, perms), true)
	if resp.StatusCode != 200 || !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateFolder PermissionSet not success (%d): %s", resp.StatusCode, out)
	}
	got := folderPerms(t, dir, fid)
	if want := mapi.NormalizeRights(mapi.RightsReviewer, true); got["default"] != want {
		t.Errorf("default rights = %#x, want Reviewer %#x", got["default"], want)
	}
	if want := mapi.NormalizeRights(mapi.RightsAuthor, true); got["alice@hermex.test"] != want {
		t.Errorf("alice rights = %#x, want Author %#x", got["alice@hermex.test"], want)
	}

	// A second set with only the default member replaces the whole ACL.
	perms2 := permissionXML(`<t:DistinguishedUser>Default</t:DistinguishedUser>`, "Editor")
	if _, out := soapPost(t, ts, updatePermsReq(`<t:FolderId Id="`+box+`"/>`, perms2), true); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("second UpdateFolder not success: %s", out)
	}
	got = folderPerms(t, dir, fid)
	if _, ok := got["alice@hermex.test"]; ok {
		t.Error("a full replace must drop alice from the ACL")
	}
	if want := mapi.NormalizeRights(mapi.RightsEditor, true); got["default"] != want {
		t.Errorf("default rights after replace = %#x, want Editor %#x", got["default"], want)
	}
}

// TestUpdateFolderPermissionSetUnknownMemberSkipped confirms a member whose address
// does not resolve in the directory is skipped, while resolvable members still apply.
func TestUpdateFolderPermissionSetUnknownMemberSkipped(t *testing.T) {
	ts, dir := seededEWS(t)
	box := createUserFolder(t, ts, "inbox", "Shared")
	fid, err := oxews.DecodeFolderID(box)
	if err != nil {
		t.Fatal(err)
	}
	perms := permissionXML(`<t:DistinguishedUser>Default</t:DistinguishedUser>`, "Reviewer") +
		permissionXML(`<t:PrimarySmtpAddress>nobody@hermex.test</t:PrimarySmtpAddress>`, "Author")
	if _, out := soapPost(t, ts, updatePermsReq(`<t:FolderId Id="`+box+`"/>`, perms), true); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateFolder not success: %s", out)
	}
	got := folderPerms(t, dir, fid)
	if _, ok := got["nobody@hermex.test"]; ok {
		t.Error("an unresolvable member must be skipped")
	}
	if _, ok := got["default"]; !ok {
		t.Error("the resolvable default member must still be applied")
	}
}

// TestUpdateFolderPermissionSetDistinguishedAllowed confirms a permission write to a
// well-known folder is allowed (unlike a rename, which is refused) — sharing a
// distinguished folder is legitimate and does not touch the name projection.
func TestUpdateFolderPermissionSetDistinguishedAllowed(t *testing.T) {
	ts, dir := seededEWS(t)
	perms := permissionXML(`<t:DistinguishedUser>Default</t:DistinguishedUser>`, "Reviewer")
	req := updatePermsReq(`<t:DistinguishedFolderId Id="inbox"/>`, perms)
	if _, out := soapPost(t, ts, req, true); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("permission change on a distinguished folder must succeed: %s", out)
	}
	if want := mapi.NormalizeRights(mapi.RightsReviewer, true); folderPerms(t, dir, int64(mapi.PrivateFIDInbox))["default"] != want {
		t.Errorf("inbox default rights not stored as Reviewer %#x", want)
	}
}
