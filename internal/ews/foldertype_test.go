package ews

import (
	"regexp"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// TestGetFolderNamesTheFolderKind proves a folder is served as the element of
// its kind with its class, which is how an EWS client finds the calendar and
// the contacts, and that a calendar or a contacts folder carries no
// UnreadCount, which their types do not have.
func TestGetFolderNamesTheFolderKind(t *testing.T) {
	ts, _ := seededEWS(t)
	cases := []struct {
		id, element, class string
		unread             bool
	}{
		{"inbox", "Folder", "IPF.Note", true},
		{"calendar", "CalendarFolder", "IPF.Appointment", false},
		{"contacts", "ContactsFolder", "IPF.Contact", false},
		{"tasks", "TasksFolder", "IPF.Task", true},
	}
	for _, c := range cases {
		_, out := soapPost(t, ts, distinguishedGetFolder(c.id), true)
		if !strings.Contains(out, "<"+c.element+` xmlns="`+nsTypes+`">`) ||
			!strings.Contains(out, "<FolderClass>"+c.class+"</FolderClass>") {
			t.Errorf("%s: GetFolder = %s, want a %s of class %s", c.id, out, c.element, c.class)
		}
		if got := strings.Contains(out, "<UnreadCount>"); got != c.unread {
			t.Errorf("%s: UnreadCount present = %v, want %v: %s", c.id, got, c.unread, out)
		}
	}
}

var createdIDRE = regexp.MustCompile(`<FolderId Id="([^"]+)"`)

// TestCreateFolderMakesTheFolderKindAsked proves CreateFolder creates a folder
// of the kind its element or its FolderClass names, stored with that class so
// every protocol sees the same kind, and refuses an element that is no kind.
func TestCreateFolderMakesTheFolderKindAsked(t *testing.T) {
	ts, dir := seededEWS(t)
	cases := []struct{ folder, element, class string }{
		{`<t:CalendarFolder><t:DisplayName>Team</t:DisplayName></t:CalendarFolder>`, "CalendarFolder", mapi.ContainerClassAppointment},
		{`<t:Folder><t:FolderClass>IPF.Contact</t:FolderClass><t:DisplayName>Clients</t:DisplayName></t:Folder>`, "ContactsFolder", mapi.ContainerClassContact},
		{`<t:TasksFolder><t:DisplayName>Chores</t:DisplayName></t:TasksFolder>`, "TasksFolder", mapi.ContainerClassTask},
		{`<t:Folder><t:DisplayName>Mail</t:DisplayName></t:Folder>`, "Folder", mapi.ContainerClassNote},
	}
	for _, c := range cases {
		_, out := soapPost(t, ts, wrapRequest(`<CreateFolder xmlns="`+nsMessages+`" xmlns:t="`+nsTypes+`">`+
			`<ParentFolderId><t:DistinguishedFolderId Id="msgfolderroot"/></ParentFolderId>`+
			`<Folders>`+c.folder+`</Folders></CreateFolder>`), true)
		id := createdIDRE.FindStringSubmatch(out)
		if len(id) != 2 || !strings.Contains(out, "<"+c.element+" ") {
			t.Errorf("CreateFolder %s = %s, want a %s", c.folder, out, c.element)
			continue
		}
		if got := storedFolderClass(t, dir, id[1]); got != c.class {
			t.Errorf("CreateFolder %s stored class %q, want %q", c.folder, got, c.class)
		}
	}
	_, out := soapPost(t, ts, wrapRequest(`<CreateFolder xmlns="`+nsMessages+`" xmlns:t="`+nsTypes+`">`+
		`<ParentFolderId><t:DistinguishedFolderId Id="msgfolderroot"/></ParentFolderId>`+
		`<Folders><t:Drawer><t:DisplayName>Odd</t:DisplayName></t:Drawer></Folders></CreateFolder>`), true)
	if !strings.Contains(out, "<ResponseCode>ErrorInvalidRequest</ResponseCode>") {
		t.Errorf("CreateFolder of an unknown kind = %s, want ErrorInvalidRequest", out)
	}
}

// storedFolderClass reads the container class the store holds for an EWS folder id.
func storedFolderClass(t *testing.T, dir, id string) string {
	t.Helper()
	fid, _, err := oxews.DecodeFolderID(id)
	if err != nil {
		t.Fatalf("decode %s: %v", id, err)
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	props, err := st.GetFolderProperties(fid, mapi.PrContainerClass)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := props.Get(mapi.PrContainerClass)
	class, _ := v.(string)
	return class
}
