package directory

import "testing"

// TestAdminTaskQueue proves the async task lifecycle: enqueue pending, atomically
// claim to running (and only once), then finish to a terminal status with a
// message. It is the directory backend for the admin Task queue.
func TestAdminTaskQueue(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM admin_tasks"); err != nil {
		t.Fatal(err)
	}

	id, err := d.CreateTask("ldapsync", "1", "admin@test")
	if err != nil {
		t.Fatal(err)
	}

	tasks, err := d.ListTasks(10)
	if err != nil || len(tasks) != 1 || tasks[0].Status != TaskPending || tasks[0].Type != "ldapsync" {
		t.Fatalf("ListTasks = %+v (err %v), want one pending ldapsync", tasks, err)
	}

	claimed, ok, err := d.ClaimNextTask()
	if err != nil || !ok || claimed.ID != id || claimed.Status != TaskRunning {
		t.Fatalf("ClaimNextTask = %+v ok=%v err=%v, want the task running", claimed, ok, err)
	}

	// The claimed task is no longer pending, so a second claim finds nothing.
	if _, ok, err := d.ClaimNextTask(); err != nil || ok {
		t.Fatalf("second ClaimNextTask ok=%v err=%v, want none pending", ok, err)
	}

	if err := d.FinishTask(id, TaskDone, "synced 5 users"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.GetTask(id)
	if err != nil || !ok || got.Status != TaskDone || got.Message != "synced 5 users" {
		t.Fatalf("GetTask = %+v ok=%v err=%v, want done with the message", got, ok, err)
	}
}
