package directory

import "testing"

func setupAutoReplySettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM autoreply_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestAutoReplySettingsRoundTrip proves a fresh database reports no settings, so
// the MTA keeps its built-in prefix, and that a saved row reads back field for
// field.
func TestAutoReplySettingsRoundTrip(t *testing.T) {
	d := setupAutoReplySettings(t)

	if _, found, err := d.GetAutoReplySettings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
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

	// A second save replaces the row rather than adding one, which is what makes
	// the MTA's poll see exactly one answer.
	if err := d.SetAutoReplySettings(AutoReplySettings{SubjectPrefix: "Out of office"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := d.GetAutoReplySettings(); got.SubjectPrefix != "Out of office" {
		t.Errorf("prefix = %q after the second save", got.SubjectPrefix)
	}
}
