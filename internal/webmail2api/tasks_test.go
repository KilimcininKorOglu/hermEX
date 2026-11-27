package webmail2api

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"
)

// TestTaskRichFieldsRoundTrip proves the task's start date, priority, reminder,
// and categories survive a create-then-reload through the oxtask named-property
// model, so the form is not a no-op after a refresh. Importance (PR_IMPORTANCE)
// maps 0=low, 1=normal, 2=high; the SPA sends 2 (high) and reads it back.
func TestTaskRichFieldsRoundTrip(t *testing.T) {
	do := apiDo(t)

	body := `{"summary":"Ship report","description":"Q3 numbers","start":"2026-07-01","due":"2026-07-15","status":1,"percent":40,"priority":2,"reminder":true,"categories":["Urgent","Finance"],"recurrence":"FREQ=WEEKLY;INTERVAL=2;COUNT=5"}`
	wantStatus(t, "create", do(http.MethodPost, "/api/v1/tasks", body), http.StatusOK)

	c := listOneTask(t, do, "list")
	wantEq(t, "summary", c.Summary, "Ship report")
	wantEq(t, "description", c.Description, "Q3 numbers")
	wantEq(t, "start", c.Start, "2026-07-01")
	wantEq(t, "due", c.Due, "2026-07-15")
	wantPriority(t, c, 2)
	wantEq(t, "status", c.Status, 1)
	wantEq(t, "percent", c.Percent, 40)
	wantEq(t, "recurrence", c.Recurrence, "FREQ=WEEKLY;INTERVAL=2;COUNT=5")
	wantEq(t, "reminder", c.Reminder, true)
	if len(c.Categories) != 2 || c.Categories[0] != "Urgent" || c.Categories[1] != "Finance" {
		t.Errorf("categories = %v, want [Urgent Finance]", c.Categories)
	}
}

// wantPriority holds a listed task's priority to want.
func wantPriority(t *testing.T, c taskJSON, want int) {
	t.Helper()
	if c.Priority == nil || *c.Priority != want {
		t.Errorf("priority = %v, want %d", c.Priority, want)
	}
}

// TestTaskPriorityLowAndUnset sends a Low priority back as 0 rather than
// dropping it, leaves an unset priority out rather than sending -1, and keeps a
// stored priority when an edit names none.
func TestTaskPriorityLowAndUnset(t *testing.T) {
	do := apiDo(t)
	low := okBody[taskJSON](t, "create low", do(http.MethodPost, "/api/v1/tasks", `{"summary":"Low","priority":0}`))
	wantPriority(t, listOneTask(t, do, "list low"), 0)

	okBody[taskJSON](t, "edit without priority", do(http.MethodPut, "/api/v1/tasks/"+low.UID, `{"summary":"Low 2"}`))
	wantPriority(t, listOneTask(t, do, "list after edit"), 0)

	wantStatus(t, "delete", do(http.MethodDelete, "/api/v1/tasks/"+low.UID, ""), http.StatusOK)
	okBody[taskJSON](t, "create unset", do(http.MethodPost, "/api/v1/tasks", `{"summary":"Unset"}`))
	c := listOneTask(t, do, "list unset")
	if c.Priority != nil || c.AcceptState != 0 {
		t.Errorf("unset task lists priority %v and acceptState %d, want both absent", c.Priority, c.AcceptState)
	}
}

// TestTaskRefusesOutOfRangeValues: MS-OXOTASK PidLidPercentComplete is 0..1
// (0..100 on the wire) and PR_IMPORTANCE is 0..2; values outside are refused and
// an update of an unknown task changes nothing.
func TestTaskRefusesOutOfRangeValues(t *testing.T) {
	do := apiDo(t)
	for name, body := range map[string]string{
		"percent above 100": `{"summary":"x","percent":101}`,
		"negative percent":  `{"summary":"x","percent":-1}`,
		"priority 3":        `{"summary":"x","priority":3}`,
	} {
		if rec := do(http.MethodPost, "/api/v1/tasks", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
	created := okBody[taskJSON](t, "create", do(http.MethodPost, "/api/v1/tasks", `{"summary":"keep","percent":10}`))
	if rec := do(http.MethodPut, "/api/v1/tasks/"+created.UID, `{"summary":"keep","percent":250}`); rec.Code != http.StatusBadRequest {
		t.Errorf("out-of-range update: status %d, want 400", rec.Code)
	}
	if rec := do(http.MethodPut, "/api/v1/tasks/987654", `{"summary":"ghost"}`); rec.Code != http.StatusNotFound {
		t.Errorf("update of an unknown task: status %d, want 404", rec.Code)
	}
	c := listOneTask(t, do, "list after refusals")
	wantEq(t, "summary after refusals", c.Summary, "keep")
	wantEq(t, "percent after refusals", c.Percent, 10)
}

// TestTaskSaveFailureIsRecorded: a task the store cannot save answers 500 and
// leaves a record of the cause.
func TestTaskSaveFailureIsRecorded(t *testing.T) {
	dir := freshMailbox(t)
	db, err := sql.Open("sqlite", filepath.Join(dir, "objects.sqlite3"))
	mustNoErr(t, "open objects db", err)
	_, err = db.Exec(`DROP TABLE messages`)
	mustNoErr(t, "drop messages", err)
	mustNoErr(t, "close objects db", db.Close())
	sink := withSink(t)
	wantStatus(t, "create", apiHarnessFor(t, dir)(http.MethodPost, "/api/v1/tasks", `{"summary":"x"}`), http.StatusInternalServerError)
	if _, ok := sink.find("task-create"); !ok {
		t.Error("the failed task save left no task-create record")
	}
}

// listOneTask reads the task listing and requires it to hold exactly one task.
func listOneTask(t *testing.T, do requestFunc, what string) taskJSON {
	t.Helper()
	type listing struct {
		Tasks []taskJSON `json:"tasks"`
	}
	tasks := okBody[listing](t, what, do(http.MethodGet, "/api/v1/tasks", "")).Tasks
	if len(tasks) != 1 {
		t.Fatalf("%s: got %d tasks, want 1", what, len(tasks))
	}
	return tasks[0]
}

// TestTaskAssignmentRoundTrip proves the assignment spine (Owner, Assigner,
// AcceptanceState) survives a create-then-reload through the oxtask named-property
// model, so a task assigned in webmail reaches EAS/EWS/MAPI with the same owner and
// acceptance state instead of a webmail-only field.
func TestTaskAssignmentRoundTrip(t *testing.T) {
	do := apiDo(t)

	// Alice assigns her task to bob; acceptance starts unknown (1).
	body := `{"summary":"Review PR","owner":"bob@hermex.test","assigner":"alice@hermex.test","acceptState":1,"completed":false}`
	wantStatus(t, "create", do(http.MethodPost, "/api/v1/tasks", body), http.StatusOK)

	c := listOneTask(t, do, "list")
	wantEq(t, "Owner", c.Owner, "bob@hermex.test")
	wantEq(t, "Assigner", c.Assigner, "alice@hermex.test")
	wantEq(t, "AcceptState (unknown)", c.AcceptState, 1)

	// Bob accepts the task: the owner stays bob, acceptance becomes 2.
	update := `{"summary":"Review PR","owner":"bob@hermex.test","assigner":"alice@hermex.test","acceptState":2,"completed":false}`
	after := okBody[taskJSON](t, "update", do(http.MethodPut, "/api/v1/tasks/"+c.UID, update))
	wantEq(t, "AcceptState after accepting", after.AcceptState, 2)
	wantEq(t, "Owner after accepting", after.Owner, "bob@hermex.test")
}
