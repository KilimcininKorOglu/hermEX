package ews

import (
	"net/http"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// createNoteReq is a CreateItem of one base Item with the given item body.
func createNoteReq(item string) string {
	return wrapRequest(`<CreateItem xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" MessageDisposition="SaveOnly">` +
		`<Items><t:Item>` + item + `</t:Item></Items></CreateItem>`)
}

// setNoteField is a SetItemField carrying value in a base <t:Item>.
func setNoteField(uri, value string) string {
	return `<t:SetItemField><t:FieldURI FieldURI="` + uri + `"/><t:Item>` + value + `</t:Item></t:SetItemField>`
}

// onlyNote opens the one note Notes holds.
func onlyNote(t *testing.T, st *objectstore.Store) (int64, mapi.PropertyValues) {
	t.Helper()
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDNotes))
	mustNoErr(t, "list Notes", err)
	if len(objs) != 1 {
		t.Fatalf("Notes holds %d items, want one note", len(objs))
	}
	msg, err := st.OpenMessage(objs[0].ID)
	mustNoErr(t, "open the note", err)
	return objs[0].ID, msg.Props
}

// TestCreateAndUpdateANoteOverEWS proves a sticky note created over EWS is the
// stored IPM.StickyNote every protocol reads, and that UpdateItem edits it in
// place. CreateItem dropped a base Item and UpdateItem sent a note to the calendar
// edit, so an EWS client could neither add nor change a note.
func TestCreateAndUpdateANoteOverEWS(t *testing.T) {
	ts, dir := seededWithMessage(t)
	resp, out := soapPost(t, ts, createNoteReq(`<t:ItemClass>IPM.StickyNote</t:ItemClass>`+
		`<t:Subject>Groceries</t:Subject><t:Body BodyType="Text">milk</t:Body>`+
		`<t:Categories><t:String>Home</t:String></t:Categories>`), true)
	wantEq(t, "CreateItem note status", resp.StatusCode, http.StatusOK)
	wantContains(t, "the create", out, `ResponseClass="Success"`)

	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	id, props := onlyNote(t, st)
	wantEq(t, "the class", strProp(props, mapi.PrMessageClass), "IPM.StickyNote")
	wantEq(t, "the subject", strProp(props, mapi.PrSubject), "Groceries")
	wantEq(t, "the body", strProp(props, mapi.PrBody), "milk")
	wantEq(t, "the categories", len(noteKeywords(st, props)), 1)
	mustCloseStore(t, st)

	itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDNotes), MessageID: id})
	resp, out = soapPost(t, ts, updateTaskReq(itemID,
		setNoteField("item:Body", `<t:Body BodyType="Text">milk and eggs</t:Body>`)+
			`<t:DeleteItemField><t:FieldURI FieldURI="item:Categories"/></t:DeleteItemField>`), true)
	wantEq(t, "UpdateItem note status", resp.StatusCode, http.StatusOK)
	wantContains(t, "the update", out, `ResponseClass="Success"`)

	st, err = objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer mustCloseStore(t, st)
	got, props := onlyNote(t, st)
	wantEq(t, "the note keeps its id", got, id)
	wantEq(t, "the edited body", strProp(props, mapi.PrBody), "milk and eggs")
	wantEq(t, "the untouched subject", strProp(props, mapi.PrSubject), "Groceries")
	wantEq(t, "the removed categories", len(noteKeywords(st, props)), 0)
}

// TestCreateItemRefusesABaseItemItCannotStore proves a base Item that is not a
// sticky note, or carries a field a note does not store, is refused.
func TestCreateItemRefusesABaseItemItCannotStore(t *testing.T) {
	ts, dir := seededWithMessage(t)
	for name, item := range map[string]string{
		"another class":   `<t:ItemClass>IPM.Post</t:ItemClass><t:Subject>x</t:Subject>`,
		"unknown element": `<t:Subject>x</t:Subject><t:ReminderIsSet>true</t:ReminderIsSet>`,
	} {
		resp, out := soapPost(t, ts, createNoteReq(item), true)
		wantEq(t, name+" status", resp.StatusCode, http.StatusOK)
		wantContains(t, name, out, "ErrorInvalidPropertySet")
	}
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer mustCloseStore(t, st)
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDNotes))
	mustNoErr(t, "list Notes", err)
	wantEq(t, "the stored notes", len(objs), 0)
}
