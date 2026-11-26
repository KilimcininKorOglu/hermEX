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

	wantWatermark(t, d, "before any digest", mbox, 0)
	mustNoErr(t, "set the watermark", d.SetDigestWatermark(mbox, 42))
	wantWatermark(t, d, "after set", mbox, 42)
	mustNoErr(t, "upsert the watermark", d.SetDigestWatermark(mbox, 99))
	wantWatermark(t, d, "after upsert", mbox, 99)
	// Another mailbox keeps its own watermark: one user's digest must not skip
	// another's quarantined mail.
	wantWatermark(t, d, "of another mailbox", "/data/mb/bob", 0)
}

// wantWatermark reads one mailbox's digest watermark and compares it.
func wantWatermark(t *testing.T, d *SQLDirectory, label, mbox string, want uint32) {
	t.Helper()
	uid, err := d.GetDigestWatermark(mbox)
	mustNoErr(t, "read the watermark "+label, err)
	wantEq(t, "watermark "+label, uid, want)
}
