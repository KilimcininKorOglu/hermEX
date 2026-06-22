package directory

import "testing"

func setupGreylist(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM greylist_triplets"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestGreylistLifecycle proves the first-contact → retry → confirmed lifecycle: a
// new triplet is unseen, recording it stores an unconfirmed first-seen time, and
// confirming it flips the flag.
func TestGreylistLifecycle(t *testing.T) {
	d := setupGreylist(t)
	const ip, from, to = "203.0.113.0/24", "s@ext.example", "u@local.example"

	if _, found, err := d.GreylistGet(ip, from, to); err != nil || found {
		t.Fatalf("Get on first contact = found %v err %v, want not found", found, err)
	}
	if err := d.GreylistUpsertSeen(ip, from, to, 1000); err != nil {
		t.Fatal(err)
	}
	g, found, err := d.GreylistGet(ip, from, to)
	if err != nil || !found {
		t.Fatalf("Get after record = found %v err %v, want found", found, err)
	}
	if g.FirstSeen != 1000 || g.Confirmed {
		t.Errorf("recorded triplet = %+v, want first_seen 1000 unconfirmed", g)
	}

	if err := d.GreylistConfirm(ip, from, to, 2000); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := d.GreylistGet(ip, from, to); !g.Confirmed {
		t.Errorf("triplet should be confirmed after the retry, got %+v", g)
	}
}

// TestGreylistUpsertKeepsOneRow proves recording the same triplet twice refreshes
// last-seen in place (the UNIQUE key) rather than inserting a duplicate, and does
// not confirm it.
func TestGreylistUpsertKeepsOneRow(t *testing.T) {
	d := setupGreylist(t)
	const ip, from, to = "198.51.100.0/24", "s@ext.example", "u@local.example"
	if err := d.GreylistUpsertSeen(ip, from, to, 1000); err != nil {
		t.Fatal(err)
	}
	if err := d.GreylistUpsertSeen(ip, from, to, 1500); err != nil {
		t.Fatal(err)
	}
	g, found, err := d.GreylistGet(ip, from, to)
	if err != nil || !found || g.FirstSeen != 1000 || g.Confirmed {
		t.Errorf("after two upserts = %+v found %v err %v, want one unconfirmed row first_seen 1000", g, found, err)
	}
}

// TestGreylistPrune proves expired triplets are removed and current ones kept: an
// old unconfirmed (never retried) and a stale confirmed go; a fresh confirmed stays.
func TestGreylistPrune(t *testing.T) {
	d := setupGreylist(t)
	// Old unconfirmed: first seen long ago, never retried.
	if err := d.GreylistUpsertSeen("a/24", "s@x", "u@y", 100); err != nil {
		t.Fatal(err)
	}
	// Stale confirmed: confirmed but not seen recently.
	if err := d.GreylistUpsertSeen("b/24", "s@x", "u@y", 100); err != nil {
		t.Fatal(err)
	}
	if err := d.GreylistConfirm("b/24", "s@x", "u@y", 200); err != nil {
		t.Fatal(err)
	}
	// Fresh confirmed: seen recently.
	if err := d.GreylistUpsertSeen("c/24", "s@x", "u@y", 9000); err != nil {
		t.Fatal(err)
	}
	if err := d.GreylistConfirm("c/24", "s@x", "u@y", 9000); err != nil {
		t.Fatal(err)
	}

	// Prune: unconfirmed first seen before 5000, confirmed last seen before 5000.
	if err := d.PruneGreylist(5000, 5000); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := d.GreylistGet("a/24", "s@x", "u@y"); found {
		t.Error("old unconfirmed triplet should have been pruned")
	}
	if _, found, _ := d.GreylistGet("b/24", "s@x", "u@y"); found {
		t.Error("stale confirmed triplet should have been pruned")
	}
	if _, found, _ := d.GreylistGet("c/24", "s@x", "u@y"); !found {
		t.Error("fresh confirmed triplet should have been kept")
	}
}
