package directory

import "testing"

// aliasSyncDir builds a directory with one domain and two mailbox users, so a test can make
// one account's alias collide with the other.
func aliasSyncDir(t *testing.T) *SQLDirectory {
	t.Helper()
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "pw")
	mustCreateUser(t, d, root, "bob@hermex.test", "pw")
	return d
}

// mustSyncAliases replaces an account's aliases the downsync way and requires no error.
func mustSyncAliases(t *testing.T, d *SQLDirectory, user string, aliases []string) []string {
	t.Helper()
	skipped, found, err := d.SyncAliasesFor(user, aliases)
	mustNoErr(t, "sync the aliases of "+user, err)
	wantEq(t, "the account was found", found, true)
	return skipped
}

// TestSyncAliasesReplacesTheSet proves the directory owns the alias set: a sync writes
// exactly what it was handed.
func TestSyncAliasesReplacesTheSet(t *testing.T) {
	d := aliasSyncDir(t)
	mustSyncAliases(t, d, "alice@hermex.test", []string{"sales@hermex.test"})

	mustSyncAliases(t, d, "alice@hermex.test", []string{"office@hermex.test"})

	got, err := d.ListAliasesFor("alice@hermex.test")
	mustNoErr(t, "list the aliases", err)
	wantEq(t, "the alias count after the second sync", len(got), 1)
	wantEq(t, "the surviving alias", got[0], "office@hermex.test")
}

// TestSyncAliasesSkipsAnAddressInUse is the load-bearing case: one address that belongs to
// another account must not cost the account every other alias it publishes.
func TestSyncAliasesSkipsAnAddressInUse(t *testing.T) {
	d := aliasSyncDir(t)

	mustSyncAliases(t, d, "alice@hermex.test", []string{"bob@hermex.test", "sales@hermex.test"})

	got, err := d.ListAliasesFor("alice@hermex.test")
	mustNoErr(t, "list the aliases", err)
	wantEq(t, "the usable alias was still written", len(got), 1)
	wantEq(t, "the surviving alias", got[0], "sales@hermex.test")
}

// TestSyncAliasesReportsTheSkippedAddress proves the caller learns which address was
// dropped, so the sync result can name it.
func TestSyncAliasesReportsTheSkippedAddress(t *testing.T) {
	d := aliasSyncDir(t)

	skipped := mustSyncAliases(t, d, "alice@hermex.test", []string{"bob@hermex.test", "sales@hermex.test"})

	wantEq(t, "the skipped address count", len(skipped), 1)
	wantEq(t, "the skipped address", skipped[0], "bob@hermex.test")
}

// TestSyncAliasesSkipsAForeignDomain proves an address outside the domains this server hosts
// is skipped rather than stored as an undeliverable route.
func TestSyncAliasesSkipsAForeignDomain(t *testing.T) {
	d := aliasSyncDir(t)

	skipped := mustSyncAliases(t, d, "alice@hermex.test", []string{"alice@partner.example"})

	wantEq(t, "the foreign address was skipped", len(skipped), 1)
}

// TestSyncAliasesReportsAnUnknownAccount proves a sync for an account that does not exist
// reports not-found rather than failing.
func TestSyncAliasesReportsAnUnknownAccount(t *testing.T) {
	d := aliasSyncDir(t)

	_, found, err := d.SyncAliasesFor("ghost@hermex.test", []string{"x@hermex.test"})
	mustNoErr(t, "sync an unknown account", err)

	wantEq(t, "an unknown account is not found", found, false)
}

// TestSetAliasesStillRefusesTheWholeSet proves the manual path is unchanged: an operator
// typing one bad address is told, and nothing is written.
func TestSetAliasesStillRefusesTheWholeSet(t *testing.T) {
	d := aliasSyncDir(t)

	if _, err := d.SetAliasesFor("alice@hermex.test", []string{"bob@hermex.test", "sales@hermex.test"}); err == nil {
		t.Fatal("SetAliasesFor must refuse a set containing an address already in use")
	}

	got, err := d.ListAliasesFor("alice@hermex.test")
	mustNoErr(t, "list the aliases", err)
	wantEq(t, "nothing was written", len(got), 0)
}
