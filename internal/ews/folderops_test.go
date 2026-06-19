package ews

import (
	"net/http/httptest"
	"strings"
	"testing"
)

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
