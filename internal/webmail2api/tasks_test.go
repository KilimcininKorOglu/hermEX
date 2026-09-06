package webmail2api

import (
	"net/http"
	"testing"
)

// TestTaskRichFieldsRoundTrip proves the task's start date, priority, reminder,
// and categories survive a create-then-reload through the oxtask named-property
// model, so the form is not a no-op after a refresh. Importance (PR_IMPORTANCE)
// maps 0=low, 1=normal, 2=high; the SPA sends 2 (high) and reads it back.
func TestTaskRichFieldsRoundTrip(t *testing.T) {
	do, _ := apiHarness(t)

	body := `{"summary":"Ship report","description":"Q3 numbers","start":"2026-07-01","due":"2026-07-15","status":1,"percent":40,"priority":2,"reminder":true,"categories":["Urgent","Finance"],"recurrence":"FREQ=WEEKLY;INTERVAL=2;COUNT=5"}`
	wantStatus(t, "create", do(http.MethodPost, "/api/v1/tasks", body), http.StatusOK)

	c := listOneTask(t, do, "list")
	wantEq(t, "summary", c.Summary, "Ship report")
	wantEq(t, "description", c.Description, "Q3 numbers")
	wantEq(t, "start", c.Start, "2026-07-01")
	wantEq(t, "due", c.Due, "2026-07-15")
	wantEq(t, "priority", c.Priority, 2)
	wantEq(t, "status", c.Status, 1)
	wantEq(t, "percent", c.Percent, 40)
	wantEq(t, "recurrence", c.Recurrence, "FREQ=WEEKLY;INTERVAL=2;COUNT=5")
	wantEq(t, "reminder", c.Reminder, true)
	if len(c.Categories) != 2 || c.Categories[0] != "Urgent" || c.Categories[1] != "Finance" {
		t.Errorf("categories = %v, want [Urgent Finance]", c.Categories)
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
	do, _ := apiHarness(t)

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
