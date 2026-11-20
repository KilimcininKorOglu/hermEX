package directory

import (
	"database/sql"
	"strconv"
	"testing"
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

// TestHideMaskColumnFollowsTheProperty proves users.hide_mask is always what
// HideMask decodes from the PR_ATTR_HIDDEN properties, whichever form a writer
// used: decimal, hex (base-0), the legacy boolean, an unparsable value (visible),
// and a deleted property. A GAL query filters on the column before its row limit,
// so a column out of step would hide or show the wrong users.
func TestHideMaskColumnFollowsTheProperty(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	const addr = "hidden@hermex.test"
	_, err := d.CreateContact(addr, "Hidden", "hermex.test")
	mustNoErr(t, "create contact", err)

	for _, tc := range []struct {
		name  string
		props map[uint32]string
		want  uint32
	}{
		{"decimal mask", map[uint32]string{PropAttrHiddenMask: "9"}, 0x09},
		{"hex mask", map[uint32]string{PropAttrHiddenMask: "0x03"}, 0x03},
		{"unparsable mask reads visible", map[uint32]string{PropAttrHiddenMask: "abc"}, 0},
		{"mask removed, legacy boolean set", map[uint32]string{PropAttrHiddenMask: "", PropAttrHidden: "1"}, 0x03},
		{"both removed", map[uint32]string{PropAttrHidden: ""}, 0},
	} {
		found, err := d.SetUserProperties(addr, tc.props)
		mustNoErr(t, tc.name, err)
		wantEq(t, tc.name+": user found", found, true)
		wantEq(t, tc.name+": hide_mask", storedHideMask(t, db, addr), tc.want)
	}
}

// TestSearchGALHidesBeforeTheLimit proves the hide bits a surface asks for drop
// hidden entries before the row limit. The two hidden contacts sort ahead of the
// visible one under ORDER BY username and fill the limit of 2 on their own, so a
// filter applied after the limit would answer with nothing visible. An entry
// carrying only a bit the caller did not ask for stays.
func TestSearchGALHidesBeforeTheLimit(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "caller@hermex.test", "password1")
	for _, a := range []string{"aa-hidden@hermex.test", "ab-hidden@hermex.test", "zz-visible@hermex.test"} {
		_, err := d.CreateContact(a, a, "hermex.test")
		mustNoErr(t, "create contact "+a, err)
	}
	for _, a := range []string{"aa-hidden@hermex.test", "ab-hidden@hermex.test"} {
		_, err := d.SetUserProperties(a, map[uint32]string{PropAttrHiddenMask: strconv.Itoa(int(HideFromGAL))})
		mustNoErr(t, "hide "+a, err)
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
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "caller@hermex.test", "password1")
	for _, a := range []string{"aa-x@hermex.test", "ab-x@hermex.test", "ac-x@hermex.test"} {
		_, err := d.CreateContact(a, a, "hermex.test")
		mustNoErr(t, "create contact "+a, err)
	}
	_, err := d.SetUserProperties("aa-x@hermex.test", map[uint32]string{PropAttrHiddenMask: strconv.Itoa(int(HideFromResolve))})
	mustNoErr(t, "hide aa-x", err)

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
	mustCreateDomain(t, d, root, "hermex.test")
	const hidden, shown = "hidden@hermex.test", "shown@hermex.test"
	for _, a := range []string{hidden, shown} {
		_, err := d.CreateContact(a, a, "hermex.test")
		mustNoErr(t, "create contact "+a, err)
	}
	var id int64
	mustNoErr(t, "user id", db.QueryRow(`SELECT id FROM users WHERE username = ?`, hidden).Scan(&id))
	_, err := db.Exec(`INSERT INTO user_properties (user_id, proptag, order_id, propval_str) VALUES (?, ?, 1, ?)`,
		id, PropAttrHiddenMask, strconv.Itoa(0x01))
	mustNoErr(t, "write the property behind the directory", err)
	_, err = db.Exec(`UPDATE users SET hide_mask = 8 WHERE username = ?`, shown)
	mustNoErr(t, "leave a stale column", err)

	mustNoErr(t, "ensure schema", d.EnsureSchema())
	wantEq(t, "hide_mask of the user hidden behind the directory", storedHideMask(t, db, hidden), uint32(0x01))
	wantEq(t, "hide_mask of the user with no property", storedHideMask(t, db, shown), uint32(0))
}
