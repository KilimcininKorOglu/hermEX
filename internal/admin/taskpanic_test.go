package admin

import (
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/ldapauth"
)

// panicSyncer is a directory syncer whose user sync panics, standing in for a
// fault in the code that reads an external directory's answer.
type panicSyncer struct{ fakeSyncer }

func (*panicSyncer) Sync(directory.LDAPConfig) ([]ldapauth.SyncedUser, error) {
	panic("ldap entry parser bug")
}

// TestAPanickingTaskFailsOnlyItself queues a sync task whose run panics, then a
// second task. The worker runs in the background with nothing above it, so an
// unrecovered panic would end the admin daemon and leave the task marked running
// forever. The task must instead finish as failed and the queue move on.
func TestAPanickingTaskFailsOnlyItself(t *testing.T) {
	d := boundFakeDir()
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.SetLDAPSyncer(&panicSyncer{})

	first, err := d.CreateTask("ldapsync", "", "admin@test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.CreateTask("unknown-kind", "", "admin@test")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if ran, err := srv.runNextTask(); !ran || err != nil {
			t.Fatalf("runNextTask ran=%v err=%v, want it to run a task", ran, err)
		}
	}
	got, ok, err := d.GetTask(first)
	mustNoErr(t, err, "get panicking task")
	wantTrue(t, ok, "panicking task present")
	if got.Status != directory.TaskFailed || strings.Contains(got.Message, "goroutine") {
		t.Errorf("panicking task = %+v, want failed with no stack trace in its message", got)
	}
	got2, ok2, err := d.GetTask(second)
	mustNoErr(t, err, "get second task")
	wantTrue(t, ok2, "second task present")
	if got2.Status != directory.TaskFailed {
		t.Errorf("next task = %+v, want it run after the panic", got2)
	}
}
