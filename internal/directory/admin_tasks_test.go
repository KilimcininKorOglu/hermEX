package directory

import (
	"strings"
	"testing"
)

// TestAdminTaskQueue proves the async task lifecycle: enqueue pending, atomically
// claim to running (and only once), then finish to a terminal status with a
// message. It is the directory backend for the admin Task queue.
func TestAdminTaskQueue(t *testing.T) {
	d, db := freshDirectory(t)
	res, err := db.Exec("DELETE FROM admin_tasks")
	mustNoErr(t, "clear the task queue", err)
	cleared, err := res.RowsAffected()
	mustNoErr(t, "count the cleared tasks", err)
	wantEq(t, "cleared task count is not negative", cleared >= 0, true)

	missing, found, err := d.GetTask(999999)
	mustNoErr(t, "get absent task", err)
	wantEq(t, "absent task found", found, false)
	wantEq(t, "absent task value", missing, TaskInfo{})

	id, err := d.CreateTask("ldapsync", "1", "admin@test")
	mustNoErr(t, "create task", err)

	tasks, err := d.ListTasks(10)
	mustNoErr(t, "list tasks", err)
	if len(tasks) != 1 {
		t.Fatalf("ListTasks = %+v, want one pending ldapsync", tasks)
	}
	wantEq(t, "queued task status", tasks[0].Status, TaskPending)
	wantEq(t, "queued task type", tasks[0].Type, "ldapsync")

	claimed, ok, err := d.ClaimNextTask()
	mustNoErr(t, "claim the next task", err)
	wantEq(t, "a task was claimed", ok, true)
	wantEq(t, "claimed task id", claimed.ID, id)
	wantEq(t, "claimed task status", claimed.Status, TaskRunning)

	// The claimed task is no longer pending, so a second claim finds nothing.
	secondTask, second, err := d.ClaimNextTask()
	mustNoErr(t, "claim again", err)
	wantEq(t, "the second claim found a task", second, false)
	wantEq(t, "the second claim task value", secondTask, TaskInfo{})

	mustNoErr(t, "finish task", d.FinishTask(id, TaskDone, "synced 5 users"))
	got, ok, err := d.GetTask(id)
	mustNoErr(t, "get task", err)
	wantEq(t, "the finished task was found", ok, true)
	wantEq(t, "finished task status", got.Status, TaskDone)
	wantEq(t, "finished task message", got.Message, "synced 5 users")

	// Oversized failure messages are truncated to the 512-byte column width.
	mustNoErr(t, "finish task with oversized message", d.FinishTask(id, TaskFailed, strings.Repeat("x", 600)))
	got, ok, err = d.GetTask(id)
	mustNoErr(t, "get truncated task", err)
	wantEq(t, "truncated task found", ok, true)
	wantEq(t, "truncated task status", got.Status, TaskFailed)
	wantEq(t, "truncated task message length", len(got.Message), 512)
}
