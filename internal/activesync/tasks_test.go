package activesync

import (
	"strconv"
	"strings"
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

// seedRecurringTask stores a task carrying an RRULE (the shape the web backend writes)
// so the store->EAS direction surfaces the MS-ASTASK Recurrence element.
func seedRecurringTask(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	task := oxtask.Task{
		Subject:        "Weekly status",
		Start:          time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC),
		Importance:     1,
		Sensitivity:    -1,
		RecurrenceRule: "FREQ=WEEKLY;INTERVAL=2;COUNT=5;BYDAY=MO",
	}
	props, err := oxtask.ToProps(task, st.GetNamedPropIDs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDTasks), &oxcmail.Message{Props: props}); err != nil {
		t.Fatal(err)
	}
}

// TestSyncTasksStreamsRecurrence proves a task authored in webmail with an RRULE
// surfaces over ActiveSync as the MS-ASTASK Recurrence element (Type 1 weekly,
// Interval 2, Occurrences 5, DayOfWeek Monday bit 2), so the shared RRULE reaches a
// device identically instead of being a webmail-only blob.
func TestSyncTasksStreamsRecurrence(t *testing.T) {
	ts, dir := seededServer(t)
	seedRecurringTask(t, dir)

	postCommand(t, ts, "Sync", tkReq("0"))
	_, root := postCommand(t, ts, "Sync", tkReq("1"))
	coll := respColl(t, root)
	if adds, _, _ := countCmds(coll); adds != 1 {
		t.Fatalf("got %d task adds, want 1", adds)
	}
	data := coll.Child(wbxml.ASCommands).Children[0].Child(wbxml.ASData)
	rec := data.Child(wbxml.TKRecurrence)
	if rec == nil {
		t.Fatal("no Recurrence element for a recurring task")
	}
	if got := rec.ChildText(wbxml.TKRecurType); got != "1" {
		t.Errorf("RecurType = %q, want 1 (weekly)", got)
	}
	if got := rec.ChildText(wbxml.TKRecurInterval); got != "2" {
		t.Errorf("RecurInterval = %q, want 2", got)
	}
	if got := rec.ChildText(wbxml.TKRecurOccurrences); got != "5" {
		t.Errorf("RecurOccurrences = %q, want 5", got)
	}
	if got := rec.ChildText(wbxml.TKRecurDayOfWeek); got != "2" {
		t.Errorf("RecurDayOfWeek = %q, want 2 (Monday bit)", got)
	}
	if got := rec.ChildText(wbxml.TKRecurStart); got != "2026-07-06T00:00:00.000Z" {
		t.Errorf("RecurStart = %q, want 2026-07-06T00:00:00.000Z", got)
	}
}

// TestSyncTasksClientAddRecurrence proves a device-authored recurring task stores as
// an RRULE (the shared recurrence shape), so webmail/MAPI/EWS read the same recurrence
// the device wrote. A weekly rule with an until-bound round-trips to FREQ=WEEKLY;UNTIL.
func TestSyncTasksClientAddRecurrence(t *testing.T) {
	ts, dir := seededServer(t)
	postCommand(t, ts, "Sync", tkReq("0"))
	until := wbxml.Str(wbxml.TKRecurUntil, "20261231T235959Z")
	add := wbxml.Elem(wbxml.ASAdd, wbxml.Str(wbxml.ASClientID, "cli-rec"),
		wbxml.Elem(wbxml.ASData,
			wbxml.Str(wbxml.TKSubject, "Daily standup"),
			wbxml.Elem(wbxml.TKRecurrence,
				wbxml.Str(wbxml.TKRecurType, "0"),
				wbxml.Str(wbxml.TKRecurInterval, "1"),
				until)))
	_, root := postCommand(t, ts, "Sync", tkReq("1", add))
	coll := respColl(t, root)
	addResp := coll.Child(wbxml.ASResponses).Child(wbxml.ASAdd)
	if addResp == nil || addResp.ChildText(wbxml.ASClientID) != "cli-rec" {
		t.Fatalf("no Add response for the recurring task: %+v", addResp)
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
	if task.RecurrenceRule == "" {
		t.Fatal("stored recurring task has no RecurrenceRule")
	}
	if got := task.RecurrenceRule; !strings.HasPrefix(got, "FREQ=DAILY") || !strings.Contains(got, "UNTIL=20261231T235959Z") {
		t.Errorf("stored RRULE = %q, want FREQ=DAILY;UNTIL=20261231T235959Z (INTERVAL=1 implicit)", got)
	}
}
