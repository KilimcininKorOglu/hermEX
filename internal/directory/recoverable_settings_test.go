package directory

import (
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

func setupRecoverableSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM recoverable_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestRecoverableSettingsRoundTrip proves an empty database reports no settings (so
// the sweep uses the default window) and a saved window reads back.
func TestRecoverableSettingsRoundTrip(t *testing.T) {
	d := setupRecoverableSettings(t)
	if _, found, err := d.GetRecoverableSettings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}
	if err := d.SetRecoverableSettings(RecoverableSettings{RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetRecoverableSettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v", found, err)
	}
	if got.RetentionDays != 30 {
		t.Errorf("retention = %d, want 30", got.RetentionDays)
	}
}

// TestSweepRecoverableItemsHonorsRetention proves the retention sweep purges
// soft-deleted items older than the operator-set window and keeps fresher ones, and
// that it re-reads the window each run so a change takes effect without a restart.
func TestSweepRecoverableItemsHonorsRetention(t *testing.T) {
	d := setupRecoverableSettings(t)
	root := t.TempDir()
	_, err := d.CreateDomain("hermex.test", filepath.Join(root, "domains", "hermex.test"))
	mustNoErr(t, "create domain", err)
	maildir := filepath.Join(root, "alice")
	_, err = d.CreateUser("alice@hermex.test", "pw", maildir)
	mustNoErr(t, "create user", err)
	seedAgedDumpster(t, maildir)

	// A 14-day window purges the 40-day-old item and keeps the fresh one.
	mustNoErr(t, "set a 14-day window", d.SetRecoverableSettings(RecoverableSettings{RetentionDays: 14}))
	n, err := d.SweepRecoverableItems(time.Now())
	mustNoErr(t, "sweep", err)
	wantEq(t, "items swept (only the 40-day-old one)", n, 1)
	wantEq(t, "dumpster items after the sweep (the fresh item is kept)", dumpsterCount(t, maildir), 1)

	// Re-injection: widening the window so nothing is old enough takes effect on the
	// next sweep without a restart.
	mustNoErr(t, "widen the window", d.SetRecoverableSettings(RecoverableSettings{RetentionDays: 3650}))
	n, err = d.SweepRecoverableItems(time.Now())
	mustNoErr(t, "sweep", err)
	wantEq(t, "items swept with a 10-year window", n, 0)
}

// seedAgedDumpster soft-deletes two messages and backdates one of them by 40
// days, so a retention window can tell them apart.
func seedAgedDumpster(t *testing.T, maildir string) {
	t.Helper()
	st, err := objectstore.Open(maildir)
	mustNoErr(t, "open mailbox", err)
	defer st.Close()
	trash := int64(mapi.PrivateFIDDeletedItems)
	raw := []byte("From: a@b.test\r\nSubject: x\r\n\r\nhi\r\n")
	oldInfo, err := st.AppendMessage(trash, raw, time.Now(), 0)
	mustNoErr(t, "append the old message", err)
	freshInfo, err := st.AppendMessage(trash, raw, time.Now(), 0)
	mustNoErr(t, "append the fresh message", err)
	mustNoErr(t, "soft-delete the old message", st.SoftDeleteMessage(trash, oldInfo.UID))
	mustNoErr(t, "soft-delete the fresh message", st.SoftDeleteMessage(trash, freshInfo.UID))
	// Backdate the old item's deletion stamp to 40 days ago; the fresh item keeps now.
	mustNoErr(t, "backdate the old message", st.SetMessageProperties(oldInfo.ID, mapi.PropertyValues{
		{Tag: mapi.PrDeletedOn, Value: mapi.UnixToNTTime(time.Now().Add(-40 * 24 * time.Hour))},
	}))
}

// dumpsterCount reports how many soft-deleted items a mailbox still holds.
func dumpsterCount(t *testing.T, maildir string) int {
	t.Helper()
	st, err := objectstore.Open(maildir)
	mustNoErr(t, "open mailbox", err)
	defer st.Close()
	dump, err := st.ListSoftDeleted(int64(mapi.PrivateFIDDeletedItems))
	mustNoErr(t, "list soft-deleted items", err)
	return len(dump)
}
