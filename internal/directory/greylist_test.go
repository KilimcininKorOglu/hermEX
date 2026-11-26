package directory

import (
	"database/sql"
	"testing"
)

func setupGreylist(t *testing.T) (*SQLDirectory, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	mustNoErr(t, "ensure the schema", d.EnsureSchema())
	cleanTables(t, db)
	_, err := db.Exec("DELETE FROM greylist_triplets")
	mustNoErr(t, "clear the greylist triplets", err)
	return d, db
}

// mustGreylistGet reads a triplet, failing the test on a read error.
func mustGreylistGet(t *testing.T, d *SQLDirectory, ip, from, to string) (Greylisted, bool) {
	t.Helper()
	g, found, err := d.GreylistGet(ip, from, to)
	mustNoErr(t, "read triplet "+ip+" "+from+" "+to, err)
	return g, found
}

// lastSeen reads a triplet's last_seen column, which GreylistGet does not return.
func lastSeen(t *testing.T, db *sql.DB, ip string) int64 {
	t.Helper()
	var v int64
	mustNoErr(t, "read last_seen of "+ip,
		db.QueryRow(`SELECT last_seen FROM greylist_triplets WHERE ip_key = ?`, ip).Scan(&v))
	return v
}

// TestGreylistLifecycle proves the first-contact → retry → confirmed lifecycle: a
// new triplet is unseen, recording it stores an unconfirmed first-seen time, and
// confirming it flips the flag of that triplet only.
func TestGreylistLifecycle(t *testing.T) {
	d, _ := setupGreylist(t)
	const ip, from, to = "203.0.113.0/24", "s@ext.example", "u@local.example"

	_, found := mustGreylistGet(t, d, ip, from, to)
	wantEq(t, "triplet found on first contact", found, false)
	mustNoErr(t, "record the first contact", d.GreylistUpsertSeen(ip, from, to, 1000))
	// A second recipient from the same sender and network is its own triplet.
	mustNoErr(t, "record a second recipient", d.GreylistUpsertSeen(ip, from, "v@local.example", 1000))
	g, found := mustGreylistGet(t, d, ip, from, to)
	wantEq(t, "triplet found after the record", found, true)
	wantEq(t, "recorded triplet", g, Greylisted{FirstSeen: 1000, Confirmed: false})

	mustNoErr(t, "confirm the retry", d.GreylistConfirm(ip, from, to, 2000))
	g, _ = mustGreylistGet(t, d, ip, from, to)
	wantEq(t, "triplet confirmed after the retry", g.Confirmed, true)
	other, _ := mustGreylistGet(t, d, ip, from, "v@local.example")
	wantEq(t, "the other recipient's triplet confirmed", other.Confirmed, false)
}

// TestGreylistConfirmNeedsAFirstContact proves a confirm for a triplet that was
// never deferred creates nothing: otherwise a sender could be allowlisted without
// ever waiting out the delay.
func TestGreylistConfirmNeedsAFirstContact(t *testing.T) {
	d, _ := setupGreylist(t)
	mustNoErr(t, "confirm an unseen triplet", d.GreylistConfirm("192.0.2.0/24", "s@ext.example", "u@local.example", 2000))
	_, found := mustGreylistGet(t, d, "192.0.2.0/24", "s@ext.example", "u@local.example")
	wantEq(t, "an unseen triplet exists after a confirm", found, false)
}

// TestGreylistUpsertKeepsOneRow proves recording the same triplet again refreshes
// last-seen in place (the UNIQUE key) rather than inserting a duplicate, keeps the
// first-seen time the delay is measured from, and never changes the confirmation:
// a new contact neither confirms a deferred triplet nor revokes a confirmed one.
func TestGreylistUpsertKeepsOneRow(t *testing.T) {
	d, db := setupGreylist(t)
	const ip, from, to = "198.51.100.0/24", "s@ext.example", "u@local.example"
	mustNoErr(t, "record the first contact", d.GreylistUpsertSeen(ip, from, to, 1000))
	mustNoErr(t, "record it again", d.GreylistUpsertSeen(ip, from, to, 1500))
	g, _ := mustGreylistGet(t, d, ip, from, to)
	wantEq(t, "triplet after two records", g, Greylisted{FirstSeen: 1000, Confirmed: false})
	wantEq(t, "last_seen after two records", lastSeen(t, db, ip), int64(1500))
	wantRows(t, db, "rows for the triplet", 1, `SELECT COUNT(*) FROM greylist_triplets WHERE ip_key = ?`, ip)

	mustNoErr(t, "confirm the retry", d.GreylistConfirm(ip, from, to, 2000))
	mustNoErr(t, "record a later contact", d.GreylistUpsertSeen(ip, from, to, 3000))
	g, _ = mustGreylistGet(t, d, ip, from, to)
	wantEq(t, "confirmation after a later contact", g.Confirmed, true)
	wantEq(t, "last_seen after a later contact", lastSeen(t, db, ip), int64(3000))
}

// TestGreylistPrune proves expired triplets are removed and current ones kept: an
// unconfirmed triplet is judged by when it was first seen (a deferral that never
// retried), a confirmed one by when it was last seen, and a cutoff is exclusive.
func TestGreylistPrune(t *testing.T) {
	d, _ := setupGreylist(t)
	seen := func(ip string, first, confirmAt int64) {
		t.Helper()
		mustNoErr(t, "record "+ip, d.GreylistUpsertSeen(ip, "s@x", "u@y", first))
		if confirmAt > 0 {
			mustNoErr(t, "confirm "+ip, d.GreylistConfirm(ip, "s@x", "u@y", confirmAt))
		}
	}
	seen("old-unconfirmed", 100, 0)
	// Re-seen recently but never retried past the delay: still judged by first_seen.
	seen("old-unconfirmed-reseen", 100, 0)
	mustNoErr(t, "re-see a deferred triplet", d.GreylistUpsertSeen("old-unconfirmed-reseen", "s@x", "u@y", 9000))
	seen("stale-confirmed", 100, 200)
	seen("fresh-unconfirmed", 9000, 0)
	seen("fresh-confirmed", 9000, 9000)
	seen("at-cutoff", 5000, 0)

	mustNoErr(t, "prune", d.PruneGreylist(5000, 5000))
	for ip, kept := range map[string]bool{
		"old-unconfirmed": false, "old-unconfirmed-reseen": false, "stale-confirmed": false,
		"fresh-unconfirmed": true, "fresh-confirmed": true, "at-cutoff": true,
	} {
		_, found := mustGreylistGet(t, d, ip, "s@x", "u@y")
		wantEq(t, ip+" kept by the prune", found, kept)
	}
}
