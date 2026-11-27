package directory

import (
	"strings"
	"testing"
)

func setupAutoReplySettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	res, err := db.Exec("DELETE FROM autoreply_settings")
	mustNoErr(t, "clear the auto-reply settings", err)
	cleared, err := res.RowsAffected()
	mustNoErr(t, "count the cleared rows", err)
	wantEq(t, "at most the one settings row was cleared", cleared <= 1, true)
	return d
}

// TestAutoReplySettingsRoundTrip proves a fresh database reports no settings, so
// the MTA keeps its built-in prefix, and that a saved row reads back field for
// field.
func TestAutoReplySettingsRoundTrip(t *testing.T) {
	d := setupAutoReplySettings(t)

	if empty, found, err := d.GetAutoReplySettings(); err != nil || found || empty != (AutoReplySettings{}) {
		t.Fatalf("Get on empty = %+v found %v err %v, want zero/not found", empty, found, err)
	}

	want := AutoReplySettings{SubjectPrefix: "Otomatik yanıt"}
	if err := d.SetAutoReplySettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetAutoReplySettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}

	wantAutoReplyReplaced(t, d)
}

// wantAutoReplyReplaced saves a second prefix and requires it to replace the
// row rather than add one, which is what makes the MTA's poll see exactly one
// answer.
func wantAutoReplyReplaced(t *testing.T, d *SQLDirectory) {
	t.Helper()
	if err := d.SetAutoReplySettings(AutoReplySettings{SubjectPrefix: "Out of office"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetAutoReplySettings()
	if err != nil || !found {
		t.Fatalf("Get after second save = found %v err %v", found, err)
	}
	if got.SubjectPrefix != "Out of office" {
		t.Errorf("prefix = %q after the second save", got.SubjectPrefix)
	}
	wantRows(t, d.db, "settings rows after two saves", 1, `SELECT COUNT(*) FROM autoreply_settings`)

	// A prefix longer than the 128-character column is refused, not stored cut,
	// and the saved prefix stays.
	wantErr(t, "an over-long prefix was accepted", d.SetAutoReplySettings(AutoReplySettings{SubjectPrefix: strings.Repeat("y", 129)}))
	got, found, err = d.GetAutoReplySettings()
	mustNoErr(t, "read after the refused save", err)
	wantEq(t, "the row is still found", found, true)
	wantEq(t, "prefix after the refused save", got.SubjectPrefix, "Out of office")
}
