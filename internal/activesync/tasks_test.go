package activesync

import (
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxtask"
	"hermex/internal/wbxml"
)

// seedTask stores one task through the canonical oxtask model, the same path the web
// backend writes, so reading it over ActiveSync exercises the shared object.
func seedTask(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	task := oxtask.Task{
		Subject:     "Ship release",
		Body:        "cut the tag",
		Due:         time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		Importance:  2,
		Sensitivity: -1,
		Categories:  []string{"Work"},
	}
	props, err := oxtask.ToProps(task, st.GetNamedPropIDs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDTasks), &oxcmail.Message{Props: props}); err != nil {
		t.Fatal(err)
	}
}

func tasksID() string { return strconv.FormatInt(int64(mapi.PrivateFIDTasks), 10) }

func tkReq(key string, cmds ...*wbxml.Node) *wbxml.Node {
	coll := []*wbxml.Node{wbxml.Str(wbxml.ASSyncKey, key), wbxml.Str(wbxml.ASCollectionID, tasksID())}
	if len(cmds) > 0 {
		coll = append(coll, wbxml.Elem(wbxml.ASCommands, cmds...))
	}
	return wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections, wbxml.Elem(wbxml.ASCollection, coll...)))
}

// TestSyncTasksStreamsTask confirms a stored task (written through oxtask, as the web
// backend does) syncs over ActiveSync with its fields, proving the shared-object
// invariant in the store->EAS direction.
func TestSyncTasksStreamsTask(t *testing.T) {
	ts, dir := seededServer(t)
	seedTask(t, dir)

	postCommand(t, ts, "Sync", tkReq("0"))
	_, root := postCommand(t, ts, "Sync", tkReq("1"))
	coll := respColl(t, root)
	if adds, _, _ := countCmds(coll); adds != 1 {
		t.Fatalf("got %d task adds, want 1", adds)
	}
	data := coll.Child(wbxml.ASCommands).Children[0].Child(wbxml.ASData)
	if got := data.ChildText(wbxml.TKSubject); got != "Ship release" {
		t.Errorf("Subject = %q, want Ship release", got)
	}
	if got := data.ChildText(wbxml.TKComplete); got != "0" {
		t.Errorf("Complete = %q, want 0", got)
	}
	if got := data.ChildText(wbxml.TKDueDate); got != "2026-07-01T00:00:00.000Z" {
		t.Errorf("DueDate = %q, want 2026-07-01T00:00:00.000Z", got)
	}
	if got := data.ChildText(wbxml.TKImportance); got != "2" {
		t.Errorf("Importance = %q, want 2", got)
	}
	if body := data.Child(wbxml.ABBody); body == nil || string(body.Child(wbxml.ABData).Opaque) != "cut the tag" {
		t.Errorf("task body not streamed: %+v", body)
	}
}

// TestSyncTasksClientAdd confirms a device-created task is stored through oxtask (the
// shared model the web backend reads), proving the EAS->store direction.
func TestSyncTasksClientAdd(t *testing.T) {
	ts, dir := seededServer(t)
	postCommand(t, ts, "Sync", tkReq("0"))
	add := wbxml.Elem(wbxml.ASAdd, wbxml.Str(wbxml.ASClientID, "cli-1"),
		wbxml.Elem(wbxml.ASData,
			wbxml.Str(wbxml.TKSubject, "Call dentist"),
			wbxml.Str(wbxml.TKComplete, "1"),
			wbxml.Str(wbxml.TKDueDate, "2026-07-05T00:00:00.000Z")))
	_, root := postCommand(t, ts, "Sync", tkReq("1", add))
	coll := respColl(t, root)

	addResp := coll.Child(wbxml.ASResponses).Child(wbxml.ASAdd)
	if addResp == nil || addResp.ChildText(wbxml.ASClientID) != "cli-1" {
		t.Fatalf("no Add response for the client task: %+v", addResp)
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
	task, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
	if err != nil {
		t.Fatal(err)
	}
	if task.Subject != "Call dentist" || !task.Complete {
		t.Errorf("stored task = %q complete=%v, want Call dentist / true", task.Subject, task.Complete)
	}
	if task.Due.Format("2006-01-02") != "2026-07-05" {
		t.Errorf("stored due = %v, want 2026-07-05", task.Due)
	}
}

// TestFolderSyncAdvertisesTasks confirms FolderSync exposes the Tasks collection with
// EAS folder type 7.
func TestFolderSyncAdvertisesTasks(t *testing.T) {
	ts, _ := seededServer(t)
	_, root := postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))
	changes := root.Child(wbxml.FHChanges)
	if changes == nil {
		t.Fatal("FolderSync returned no Changes")
	}
	for _, add := range changes.Children {
		if add.Tag == wbxml.FHAdd && add.ChildText(wbxml.FHServerID) == tasksID() {
			if got := add.ChildText(wbxml.FHType); got != "7" {
				t.Errorf("Tasks folder Type = %q, want 7", got)
			}
			return
		}
	}
	t.Error("FolderSync did not advertise the Tasks collection")
}
