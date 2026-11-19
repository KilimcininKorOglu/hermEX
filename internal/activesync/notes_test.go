package activesync

import (
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/wbxml"
)

// seedNote stores one IPM.StickyNote, the same shape the web backend writes (title in
// PR_SUBJECT, text in PR_BODY), plus a category and a last-modified time.
func seedNote(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	kid, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameKeywords})
	if err != nil {
		t.Fatal(err)
	}
	props := mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: noteClass},
		{Tag: mapi.PrSubject, Value: "Grocery list"},
		{Tag: mapi.PrBody, Value: "milk, eggs"},
		{Tag: mapi.PrLastModificationTime, Value: mapi.UnixToNTTime(time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC))},
		{Tag: mapi.MakeTag(kid[0], mapi.PtMvUnicode), Value: []string{"Home"}},
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDNotes), &oxcmail.Message{Props: props}); err != nil {
		t.Fatal(err)
	}
}

func notesID() string { return strconv.FormatInt(int64(mapi.PrivateFIDNotes), 10) }

func ntReq(key string, cmds ...*wbxml.Node) *wbxml.Node {
	coll := []*wbxml.Node{wbxml.Str(wbxml.ASSyncKey, key), wbxml.Str(wbxml.ASCollectionID, notesID())}
	if len(cmds) > 0 {
		coll = append(coll, wbxml.Elem(wbxml.ASCommands, cmds...))
	}
	return wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections, wbxml.Elem(wbxml.ASCollection, coll...)))
}

// TestSyncNotesStreamsNote confirms a stored note syncs over ActiveSync with its
// title, body, and category.
func TestSyncNotesStreamsNote(t *testing.T) {
	ts, dir := seededServer(t)
	seedNote(t, dir)

	postCommand(t, ts, "Sync", ntReq("0"))
	_, root := postCommand(t, ts, "Sync", ntReq("1"))
	coll := respColl(t, root)
	if adds, _, _ := countCmds(coll); adds != 1 {
		t.Fatalf("got %d note adds, want 1", adds)
	}
	data := coll.Child(wbxml.ASCommands).Children[0].Child(wbxml.ASData)
	if got := data.ChildText(wbxml.NTSubject); got != "Grocery list" {
		t.Errorf("Subject = %q, want Grocery list", got)
	}
	if body := data.Child(wbxml.ABBody); body == nil || string(body.Child(wbxml.ABData).Opaque) != "milk, eggs" {
		t.Errorf("note body not streamed: %+v", body)
	}
	cats := data.Child(wbxml.NTCategories)
	if cats == nil || len(cats.Children) != 1 || cats.Children[0].Text != "Home" {
		t.Errorf("Categories = %+v, want one Category Home", cats)
	}
}

// TestSyncNotesClientAdd confirms a device-created note is stored as an IPM.StickyNote
// with the title and body the web backend reads.
func TestSyncNotesClientAdd(t *testing.T) {
	ts, dir := seededServer(t)
	postCommand(t, ts, "Sync", ntReq("0"))
	add := wbxml.Elem(wbxml.ASAdd, wbxml.Str(wbxml.ASClientID, "cli-1"),
		wbxml.Elem(wbxml.ASData,
			wbxml.Str(wbxml.NTSubject, "Idea"),
			wbxml.Elem(wbxml.ABBody, wbxml.Str(wbxml.ABType, "1"), wbxml.Str(wbxml.ABData, "ship it"))))
	_, root := postCommand(t, ts, "Sync", ntReq("1", add))
	coll := respColl(t, root)

	addResp := coll.Child(wbxml.ASResponses).Child(wbxml.ASAdd)
	if addResp == nil || addResp.ChildText(wbxml.ASClientID) != "cli-1" {
		t.Fatalf("no Add response for the client note: %+v", addResp)
	}
	if adds, _, _ := countCmds(coll); adds != 0 {
		t.Errorf("the client's add was echoed back (%d)", adds)
	}
	id, err := strconv.ParseInt(addResp.ChildText(wbxml.ASServerID), 10, 64)
	if err != nil {
		t.Fatal(err)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msg, err := st.OpenMessage(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := contactStr(msg.Props, mapi.PrSubject); got != "Idea" {
		t.Errorf("stored Subject = %q, want Idea", got)
	}
	if got := contactStr(msg.Props, mapi.PrBody); got != "ship it" {
		t.Errorf("stored Body = %q, want ship it", got)
	}
	if got := contactStr(msg.Props, mapi.PrMessageClass); got != noteClass {
		t.Errorf("stored class = %q, want %s", got, noteClass)
	}
	// OpenMessage finds an object in any folder, so the folder the add was sent
	// to is checked on its own.
	fid, err := st.MessageFolder(id)
	mustNoErr(t, "read the note's folder", err)
	if fid != int64(mapi.PrivateFIDNotes) {
		t.Errorf("note stored in folder %d, want Notes (%d)", fid, mapi.PrivateFIDNotes)
	}
}

// TestSyncNotesRefusesAnObjectOfAnotherFolder proves a Change or Delete in the
// Notes collection whose ServerId names an object of another folder (here a
// Calendar item) is refused with Status 8, [MS-ASCMD] Sync "object not found in
// the collection", and leaves that object as it was: a ServerId is scoped to the
// collection it is sent in.
func TestSyncNotesRefusesAnObjectOfAnotherFolder(t *testing.T) {
	ts, dir := seededServer(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
		{Tag: mapi.PrSubject, Value: "Dentist"},
	}})
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	sid := strconv.FormatInt(foreign, 10)
	postCommand(t, ts, "Sync", ntReq("0"))
	change := wbxml.Elem(wbxml.ASChange, wbxml.Str(wbxml.ASServerID, sid),
		wbxml.Elem(wbxml.ASData, wbxml.Str(wbxml.NTSubject, "Overwritten")))
	_, root := postCommand(t, ts, "Sync", ntReq("1", change))
	if got := respColl(t, root).Child(wbxml.ASResponses).Child(wbxml.ASChange).ChildText(wbxml.ASStatus); got != "8" {
		t.Errorf("Change Status = %q, want 8", got)
	}
	key := respColl(t, root).ChildText(wbxml.ASSyncKey)
	del := wbxml.Elem(wbxml.ASDelete, wbxml.Str(wbxml.ASServerID, sid))
	postCommand(t, ts, "Sync", ntReq(key, del))

	st, err = objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msg, err := st.OpenMessage(foreign)
	if err != nil {
		t.Fatalf("the Calendar item is gone after a Notes Delete: %v", err)
	}
	if got := contactStr(msg.Props, mapi.PrSubject); got != "Dentist" {
		t.Errorf("Calendar item Subject = %q, want Dentist", got)
	}
}

// TestFolderSyncAdvertisesNotes confirms FolderSync exposes the Notes collection with
// EAS folder type 10.
func TestFolderSyncAdvertisesNotes(t *testing.T) {
	ts, _ := seededServer(t)
	_, root := postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))
	changes := root.Child(wbxml.FHChanges)
	if changes == nil {
		t.Fatal("FolderSync returned no Changes")
	}
	for _, add := range changes.Children {
		if add.Tag == wbxml.FHAdd && add.ChildText(wbxml.FHServerID) == notesID() {
			if got := add.ChildText(wbxml.FHType); got != "10" {
				t.Errorf("Notes folder Type = %q, want 10", got)
			}
			return
		}
	}
	t.Error("FolderSync did not advertise the Notes collection")
}
