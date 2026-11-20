package directory

import (
	"slices"
	"strings"
	"testing"
)

// aliasSyncDir builds a directory with one domain and two mailbox users, so a test can make
// one account's alias collide with the other.
func aliasSyncDir(t *testing.T) *SQLDirectory {
	t.Helper()
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "password1")
	mustCreateUser(t, d, root, "bob@hermex.test", "password1")
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

// TestSyncAliasesStayInTheAccountsOrganization gives one account aliases in
// domains of its own organization, of another organization and of no
// organization. The directory a downsync reads belongs to one customer, so an
// alias it publishes in another customer's domain would take that customer's
// mail and let the account send under that address. Only the account's own
// organization may be aliased; a domain outside any organization only itself.
func TestSyncAliasesStayInTheAccountsOrganization(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	home := mustCreateDomain(t, d, root, "hermex.test")
	sister := mustCreateDomain(t, d, root, "hermex.de")
	other := mustCreateDomain(t, d, root, "other.test")
	mustCreateDomain(t, d, root, "loose.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "password1")
	mustCreateUser(t, d, root, "carol@loose.test", "password1")
	acme, err := d.CreateOrg("acme", "")
	mustNoErr(t, "create acme", err)
	rival, err := d.CreateOrg("rival", "")
	mustNoErr(t, "create rival", err)
	for _, a := range []struct{ domain, org int64 }{{home, acme}, {sister, acme}, {other, rival}} {
		_, err := d.AssignDomainToOrg(a.domain, a.org)
		mustNoErr(t, "assign a domain", err)
	}

	skipped := mustSyncAliases(t, d, "alice@hermex.test",
		[]string{"sales@hermex.test", "sales@hermex.de", "ceo@other.test", "ceo@loose.test"})
	wantEq(t, "the aliases outside acme that were skipped", strings.Join(skipped, ","), "ceo@other.test,ceo@loose.test")
	got, err := d.ListAliasesFor("alice@hermex.test")
	mustNoErr(t, "list alice's aliases", err)
	slices.Sort(got)
	wantEq(t, "alice's aliases", strings.Join(got, ","), "sales@hermex.de,sales@hermex.test")

	skipped = mustSyncAliases(t, d, "carol@loose.test", []string{"info@loose.test", "info@hermex.test"})
	wantEq(t, "the alias outside an organizationless domain that was skipped", strings.Join(skipped, ","), "info@hermex.test")
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
