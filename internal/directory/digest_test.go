package directory

import "testing"

func setupDigest(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	for _, tbl := range []string{"digest_settings", "digest_state"} {
		if _, err := db.Exec("DELETE FROM " + tbl); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// TestDigestSettingsRoundTrip proves a fresh database reports no settings (so the MTA
// keeps the digest disabled), and that a saved row reads back field for field.
func TestDigestSettingsRoundTrip(t *testing.T) {
	d := setupDigest(t)

	if _, found, err := d.GetDigestSettings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}

	want := DigestSettings{Enabled: true, IntervalHours: 12, BaseURL: "https://mail.example.com"}
	if err := d.SetDigestSettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetDigestSettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v", found, err)
	}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

// TestDigestWatermark proves the per-mailbox watermark starts at 0 (never digested),
// round-trips a stored value, and upserts in place rather than inserting duplicates.
func TestDigestWatermark(t *testing.T) {
	d := setupDigest(t)
	const mbox = "/data/mb/alice"

	if uid, err := d.GetDigestWatermark(mbox); err != nil || uid != 0 {
		t.Fatalf("watermark before any digest = (%d, %v), want (0, nil)", uid, err)
	}
	if err := d.SetDigestWatermark(mbox, 42); err != nil {
		t.Fatal(err)
	}
	if uid, err := d.GetDigestWatermark(mbox); err != nil || uid != 42 {
		t.Fatalf("watermark after set = (%d, %v), want (42, nil)", uid, err)
	}
	if err := d.SetDigestWatermark(mbox, 99); err != nil {
		t.Fatal(err)
	}
	if uid, err := d.GetDigestWatermark(mbox); err != nil || uid != 99 {
		t.Errorf("watermark after upsert = (%d, %v), want (99, nil)", uid, err)
	}
}
