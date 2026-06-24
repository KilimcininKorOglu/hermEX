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
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "domains", "hermex.test")); err != nil {
		t.Fatalf("create domain: %v", err)
	}
	maildir := filepath.Join(root, "alice")
	if _, err := d.CreateUser("alice@hermex.test", "pw", maildir); err != nil {
		t.Fatalf("create user: %v", err)
	}

	st, err := objectstore.Open(maildir)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: a@b.test\r\nSubject: x\r\n\r\nhi\r\n")
	oldInfo, err := st.AppendMessage(int64(mapi.PrivateFIDDeletedItems), raw, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	freshInfo, err := st.AppendMessage(int64(mapi.PrivateFIDDeletedItems), raw, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SoftDeleteMessage(int64(mapi.PrivateFIDDeletedItems), oldInfo.UID); err != nil {
		t.Fatal(err)
	}
	if err := st.SoftDeleteMessage(int64(mapi.PrivateFIDDeletedItems), freshInfo.UID); err != nil {
		t.Fatal(err)
	}
	// Backdate the old item's deletion stamp to 40 days ago; the fresh item keeps now.
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := st.SetMessageProperties(oldInfo.ID, mapi.PropertyValues{
		{Tag: mapi.PrDeletedOn, Value: mapi.UnixToNTTime(old)},
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// A 14-day window purges the 40-day-old item and keeps the fresh one.
	if err := d.SetRecoverableSettings(RecoverableSettings{RetentionDays: 14}); err != nil {
		t.Fatal(err)
	}
	n, err := d.SweepRecoverableItems(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("swept %d, want 1 (only the 40-day-old item)", n)
	}
	st2, err := objectstore.Open(maildir)
	if err != nil {
		t.Fatal(err)
	}
	if dump, _ := st2.ListSoftDeleted(int64(mapi.PrivateFIDDeletedItems)); len(dump) != 1 {
		t.Errorf("dumpster = %d after sweep, want 1 (fresh item kept)", len(dump))
	}
	st2.Close()

	// Re-injection: widening the window so nothing is old enough takes effect on the
	// next sweep without a restart.
	if err := d.SetRecoverableSettings(RecoverableSettings{RetentionDays: 3650}); err != nil {
		t.Fatal(err)
	}
	if n, err := d.SweepRecoverableItems(time.Now()); err != nil || n != 0 {
		t.Errorf("swept %d (err %v) with a 10-year window, want 0", n, err)
	}
}
