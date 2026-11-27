package directory

import (
	"database/sql"
	"strconv"
	"testing"

	"hermex/internal/logging"
)

// storedHideMask reads the numeric copy of a user's hide mask.
func storedHideMask(t *testing.T, db *sql.DB, addr string) uint32 {
	t.Helper()
	var m uint32
	err := db.QueryRow(`SELECT hide_mask FROM users WHERE username = ?`, addr).Scan(&m)
	label := "read hide_mask of " + addr
	mustNoErr(t, label, err)
	return m
}

// wantAffected asserts a direct write touched exactly one row.
func wantAffected(t *testing.T, what string, res sql.Result) {
	t.Helper()
	n, err := res.RowsAffected()
	mustNoErr(t, "count "+what, err)
	wantEq(t, what, n, int64(1))
}

// TestHideMaskColumnFollowsTheProperty proves users.hide_mask is always what
// HideMask decodes from the PR_ATTR_HIDDEN properties, whichever form a writer
// used: decimal, hex (base-0), the legacy boolean, an unparsable value (visible),
// and a deleted property. A GAL query filters on the column before its row limit,
// so a column out of step would hide or show the wrong users.
func TestHideMaskColumnFollowsTheProperty(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	const addr = "hidden@hermex.test"
	cID, err := d.CreateContact(addr, "Hidden", "hermex.test")
	mustNoErr(t, "create contact", err)
	wantEq(t, "hidden contact id > 0", cID > 0, true)
	const bystander = "bystander@hermex.test"
	bID, err := d.CreateContact(bystander, "Bystander", "hermex.test")
	mustNoErr(t, "create the bystander contact", err)
	wantEq(t, "bystander contact id > 0", bID > 0, true)

	for _, tc := range []struct {
		name  string
		props map[uint32]string
		want  uint32
	}{
		{"decimal mask", map[uint32]string{PropAttrHiddenMask: "9"}, 0x09},
		{"hex mask", map[uint32]string{PropAttrHiddenMask: "0x03"}, 0x03},
		{"unparsable mask reads visible", map[uint32]string{PropAttrHiddenMask: "abc"}, 0},
		// PtLong is 32 bits: a wider value is not a mask and must not wrap.
		{"mask past 32 bits reads visible", map[uint32]string{PropAttrHiddenMask: "4294967297"}, 0},
		{"mask removed, legacy boolean set", map[uint32]string{PropAttrHiddenMask: "", PropAttrHidden: "1"}, 0x03},
		{"both removed", map[uint32]string{PropAttrHidden: ""}, 0},
	} {
		found, err := d.SetUserProperties(addr, tc.props)
		mustNoErr(t, tc.name, err)
		wantEq(t, tc.name+": user found", found, true)
		wantEq(t, tc.name+": hide_mask", storedHideMask(t, db, addr), tc.want)
		wantEq(t, tc.name+": the bystander's hide_mask", storedHideMask(t, db, bystander), uint32(0))
	}
	found, err := d.SetUserProperties("ghost@hermex.test", map[uint32]string{PropAttrHiddenMask: "9"})
	mustNoErr(t, "hide an unknown account", err)
	wantEq(t, "an unknown account was found", found, false)
	wantEq(t, "the bystander's hide_mask after the unknown account", storedHideMask(t, db, bystander), uint32(0))
}

// TestSearchGALHidesBeforeTheLimit proves the hide bits a surface asks for drop
// hidden entries before the row limit. The two hidden contacts sort ahead of the
// visible one under ORDER BY username and fill the limit of 2 on their own, so a
// filter applied after the limit would answer with nothing visible. An entry
// carrying only a bit the caller did not ask for stays.
func TestSearchGALHidesBeforeTheLimit(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	seedUser(t, d, root, "caller@hermex.test", "password1")
	for _, a := range []string{"aa-hidden@hermex.test", "ab-hidden@hermex.test", "zz-visible@hermex.test"} {
		cid, err := d.CreateContact(a, a, "hermex.test")
		mustNoErr(t, "create contact "+a, err)
		wantEq(t, "contact id > 0 for "+a, cid > 0, true)
	}
	for _, a := range []string{"aa-hidden@hermex.test", "ab-hidden@hermex.test"} {
		ok, err := d.SetUserProperties(a, map[uint32]string{PropAttrHiddenMask: strconv.Itoa(int(HideFromGAL))})
		mustNoErr(t, "hide "+a, err)
		wantEq(t, "hide contact found "+a, ok, true)
	}

	got, err := d.SearchGAL("caller@hermex.test", "-", 2, HideFromGAL)
	mustNoErr(t, "search hiding GAL entries", err)
	if len(got) != 1 || got[0].Address != "zz-visible@hermex.test" {
		t.Errorf("SearchGAL(limit 2, hide GAL) = %+v, want only zz-visible@hermex.test", got)
	}

	got, err = d.SearchGAL("caller@hermex.test", "-", 5, HideFromResolve)
	mustNoErr(t, "search hiding resolution entries", err)
	wantEq(t, "entries when the caller hides only name resolution", len(got), 3)
}

// TestCountGALCountsWhatTheSearchFinds proves CountGAL counts the rows SearchGAL
// pages through, for the same caller, query and hide bits: every match with no
// limit, hidden entries left out, and nothing for a caller outside the scope.
func TestCountGALCountsWhatTheSearchFinds(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	seedUser(t, d, root, "caller@hermex.test", "password1")
	for _, a := range []string{"aa-x@hermex.test", "ab-x@hermex.test", "ac-x@hermex.test"} {
		cid, err := d.CreateContact(a, a, "hermex.test")
		mustNoErr(t, "create contact "+a, err)
		wantEq(t, "contact id > 0 for "+a, cid > 0, true)
	}
	ok, err := d.SetUserProperties("aa-x@hermex.test", map[uint32]string{PropAttrHiddenMask: strconv.Itoa(int(HideFromResolve))})
	mustNoErr(t, "hide aa-x", err)
	wantEq(t, "hide contact found aa-x", ok, true)

	for _, c := range []struct {
		caller string
		hide   uint32
		want   int
	}{
		{"caller@hermex.test", 0, 3},
		{"caller@hermex.test", HideFromResolve, 2},
		{"", 0, 0},
	} {
		n, err := d.CountGAL(c.caller, "-x", c.hide)
		mustNoErr(t, "count", err)
		wantEq(t, "count for "+c.caller, n, c.want)
		got, err := d.SearchGAL(c.caller, "-x", 100, c.hide)
		mustNoErr(t, "search", err)
		wantEq(t, "search length for "+c.caller, len(got), n)
	}
}

// TestEnsureSchemaHealsAStaleHideMask proves a start-up recomputes the column for
// a property written behind SetUserProperties (a restored dump, or an older
// binary that knew nothing of the column), in both directions.
func TestEnsureSchemaHealsAStaleHideMask(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	const hidden, shown = "hidden@hermex.test", "shown@hermex.test"
	for _, a := range []string{hidden, shown} {
		cid, err := d.CreateContact(a, a, "hermex.test")
		mustNoErr(t, "create contact "+a, err)
		wantEq(t, "contact id > 0 for "+a, cid > 0, true)
	}
	var id int64
	mustNoErr(t, "user id", db.QueryRow(`SELECT id FROM users WHERE username = ?`, hidden).Scan(&id))
	res, err := db.Exec(`INSERT INTO user_properties (user_id, proptag, order_id, propval_str) VALUES (?, ?, 1, ?)`,
		id, PropAttrHiddenMask, strconv.Itoa(0x01))
	mustNoErr(t, "write the property behind the directory", err)
	wantAffected(t, "property rows written behind the directory", res)
	res, err = db.Exec(`UPDATE users SET hide_mask = 8 WHERE username = ?`, shown)
	mustNoErr(t, "leave a stale column", err)
	wantAffected(t, "users left with a stale column", res)

	// A refused column write is recorded and leaves the column as it was.
	sink := &captureSink{}
	d.SetLogger(logging.New(sink))
	installFailTrigger(t, db, "test_hide_mask_fail", "UPDATE", "users")
	mustNoErr(t, "ensure schema with the column write refused", d.EnsureSchema())
	e := wantWarnEvent(t, sink, "directory.hide_mask.fail")
	var shownID int64
	mustNoErr(t, "shown's id", db.QueryRow(`SELECT id FROM users WHERE username = ?`, shown).Scan(&shownID))
	if u := e.Fields["user_id"]; u != any(id) && u != any(shownID) {
		t.Errorf("the hide mask failure names user %v, want %d or %d", u, id, shownID)
	}
	wantEq(t, "hide_mask after the refused write", storedHideMask(t, db, shown), uint32(8))
	execRows(t, "drop the failure trigger", 0)(db.Exec("DROP TRIGGER `test_hide_mask_fail`"))

	// A candidate list that cannot be read is recorded and changes no column.
	unlisted := &captureSink{}
	d.SetLogger(logging.New(unlisted))
	restore := hideTable(t, db, "RENAME TABLE user_properties TO user_properties_hidden",
		"RENAME TABLE user_properties_hidden TO user_properties")
	d.syncHideMasks()
	restore()
	wantWarnEvent(t, unlisted, "directory.hide_mask_list.fail")
	wantEq(t, "hide_mask after the unreadable list", storedHideMask(t, db, shown), uint32(8))

	mustNoErr(t, "ensure schema", d.EnsureSchema())
	wantEq(t, "hide_mask of the user hidden behind the directory", storedHideMask(t, db, hidden), uint32(0x01))
	wantEq(t, "hide_mask of the user with no property", storedHideMask(t, db, shown), uint32(0))
}
