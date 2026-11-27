package directory

import (
	"errors"
	"os"
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
	clearSettingsRow(t, db.Exec, "DELETE FROM recoverable_settings")
	return d
}

// TestRecoverableSettingsRoundTrip proves an empty database reports no settings (so
// the sweep uses the default window) and a saved window reads back.
func TestRecoverableSettingsRoundTrip(t *testing.T) {
	d := setupRecoverableSettings(t)
	if empty, found, err := d.GetRecoverableSettings(); err != nil || found || empty != (RecoverableSettings{}) {
		t.Fatalf("Get on empty = %+v found %v err %v, want zero not found", empty, found, err)
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
	did, err := d.CreateDomain("hermex.test", filepath.Join(root, "domains", "hermex.test"))
	mustNoErr(t, "create domain", err)
	wantEq(t, "domain id > 0", did > 0, true)
	maildir := filepath.Join(root, "alice")
	uid, err := d.CreateUser("alice@hermex.test", "password1", maildir)
	mustNoErr(t, "create user", err)
	wantEq(t, "user id > 0", uid > 0, true)
	seedAgedDumpster(t, maildir)

	// A 14-day window purges the 40-day-old item and keeps the fresh one.
	mustNoErr(t, "set a 14-day window", d.SetRecoverableSettings(RecoverableSettings{RetentionDays: 14}))
	n, err := d.SweepRecoverableItems(time.Now())
	mustNoErr(t, "sweep", err)
	wantEq(t, "items swept (only the 40-day-old one)", n, 1)
	kept := dumpster(t, maildir)
	wantEq(t, "dumpster items after the sweep (the fresh item is kept)", len(kept), 1)
	if age := time.Since(kept[0].DeletedOn); age > time.Hour {
		t.Errorf("the sweep kept an item deleted %v ago, want the fresh one", age)
	}

	// Zero turns the purge off: nothing is swept, however old.
	mustNoErr(t, "turn the purge off", d.SetRecoverableSettings(RecoverableSettings{RetentionDays: 0}))
	n, err = d.SweepRecoverableItems(time.Now().Add(100 * 365 * 24 * time.Hour))
	mustNoErr(t, "sweep with the purge off", err)
	wantEq(t, "items swept with the purge off", n, 0)

	// Re-injection: widening the window so nothing is old enough takes effect on the
	// next sweep without a restart.
	mustNoErr(t, "widen the window", d.SetRecoverableSettings(RecoverableSettings{RetentionDays: 3650}))
	n, err = d.SweepRecoverableItems(time.Now())
	mustNoErr(t, "sweep", err)
	wantEq(t, "items swept with a 10-year window", n, 0)

	// A window past what time.Duration holds (about 106751 days) keeps everything
	// too: it must never wrap into a cutoff in the future that purges the dumpster.
	// 150000 days times 24 h wraps int64 nanoseconds to a negative duration.
	mustNoErr(t, "set a 150000-day window", d.SetRecoverableSettings(RecoverableSettings{RetentionDays: 150000}))
	n, err = d.SweepRecoverableItems(time.Now())
	mustNoErr(t, "sweep with a very wide window", err)
	wantEq(t, "items swept with a 150000-day window", n, 0)
	wantEq(t, "dumpster items after the very wide window", len(dumpster(t, maildir)), 1)
}

// TestMaintenanceReadsNeverCreateAMailbox proves the retention sweep and the
// public-delegate read leave a missing mailbox missing. A mailbox is made when
// its account is created; one that is gone (a deleted directory, an unmounted
// volume) must not be replaced by an empty mailbox at that path.
func TestMaintenanceReadsNeverCreateAMailbox(t *testing.T) {
	d := setupRecoverableSettings(t)
	root := t.TempDir()
	did, err := d.CreateDomain("hermex.test", filepath.Join(root, "domains", "hermex.test"))
	mustNoErr(t, "create domain", err)
	wantEq(t, "domain id > 0", did > 0, true)
	maildir := filepath.Join(root, "alice")
	uid, err := d.CreateUser("alice@hermex.test", "password1", maildir)
	mustNoErr(t, "create user", err)
	wantEq(t, "user id > 0", uid > 0, true)
	mustNoErr(t, "remove the mailbox", os.RemoveAll(maildir))
	mustNoErr(t, "set a window", d.SetRecoverableSettings(RecoverableSettings{RetentionDays: 14}))

	swept, err := d.SweepRecoverableItems(time.Now())
	mustNoErr(t, "sweep", err)
	wantEq(t, "swept count on missing mailbox", swept, 0)
	list, err := d.Delegates("alice@hermex.test")
	mustNoErr(t, "delegates", err)
	if len(list) != 0 {
		t.Errorf("delegates of a missing mailbox = %v, want none", list)
	}
	add := func(list []string) []string { return append(list, "bob@hermex.test") }
	if err := d.UpdateDelegates("alice@hermex.test", add); !errors.Is(err, objectstore.ErrNotProvisioned) {
		t.Errorf("a delegate write to a missing mailbox = %v, want ErrNotProvisioned", err)
	}
	if _, err := os.Stat(maildir); !os.IsNotExist(err) {
		t.Errorf("a maintenance read or write created the missing mailbox: %v", err)
	}
}

// seedAgedDumpster soft-deletes two messages and backdates one of them by 40
// days, so a retention window can tell them apart.
func seedAgedDumpster(t *testing.T, maildir string) {
	t.Helper()
	st, err := objectstore.Open(maildir)
	mustNoErr(t, "open mailbox", err)
	defer mustClose(t, st)
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

// dumpster lists the soft-deleted items a mailbox still holds.
func dumpster(t *testing.T, maildir string) []objectstore.SoftDeletedMessage {
	t.Helper()
	st, err := objectstore.OpenExisting(maildir)
	mustNoErr(t, "open mailbox", err)
	defer mustClose(t, st)
	dump, err := st.ListSoftDeleted(int64(mapi.PrivateFIDDeletedItems))
	mustNoErr(t, "list soft-deleted items", err)
	return dump
}

// TestRecoverableSettingsRefuseANegativeWindow proves a negative window is refused
// and the stored one kept: zero is the documented "never purge" value, and a
// negative number would turn the purge off without anyone choosing that.
func TestRecoverableSettingsRefuseANegativeWindow(t *testing.T) {
	d := setupRecoverableSettings(t)
	mustNoErr(t, "save a window", d.SetRecoverableSettings(RecoverableSettings{RetentionDays: 30}))
	wantErr(t, "a negative window was accepted", d.SetRecoverableSettings(RecoverableSettings{RetentionDays: -1}))
	got, found, err := d.GetRecoverableSettings()
	mustNoErr(t, "read the window", err)
	wantEq(t, "window found", found, true)
	wantEq(t, "window after the refusal", got.RetentionDays, 30)
}
