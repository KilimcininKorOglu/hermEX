package directory

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

// aliasTestDir seeds one hosted domain with two accounts, the smallest directory
// in which an alias can collide with something real.
func aliasTestDir(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "dom")); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"alice@hermex.test", "bob@hermex.test"} {
		if _, err := d.CreateUser(u, "secret", filepath.Join(root, u)); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// TestAliasOverAnAccountIsRefused proves an alias can no longer disable the
// account it collides with. Resolution reads username, alternative name and alias
// in one union and treats two matches as no match, so binding an alias over an
// existing address made BOTH unreachable: bob stopped receiving mail and stopped
// being able to sign in, and nothing reported why.
func TestAliasOverAnAccountIsRefused(t *testing.T) {
	d := aliasTestDir(t)

	if err := d.CreateAlias("bob@hermex.test", "alice@hermex.test"); err == nil {
		t.Fatal("an alias over an existing account was accepted")
	}
	if _, ok := d.Resolve("bob@hermex.test"); !ok {
		t.Error("bob no longer resolves, so the collision was written anyway")
	}
	if _, ok := d.Authenticate("bob@hermex.test", "secret"); !ok {
		t.Error("bob can no longer sign in, so the collision was written anyway")
	}
}

// TestAliasOverAnotherAliasIsRefused proves the same protection covers the other
// two resolution keys, which collide exactly the same way.
func TestAliasOverAnotherAliasIsRefused(t *testing.T) {
	d := aliasTestDir(t)
	if err := d.CreateAlias("sales@hermex.test", "alice@hermex.test"); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateAlias("sales@hermex.test", "bob@hermex.test"); err == nil {
		t.Error("an alias over another alias was accepted")
	}
	if _, err := d.SetAltnames("bob@hermex.test", []string{"bobby@hermex.test"}); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateAlias("bobby@hermex.test", "alice@hermex.test"); err == nil {
		t.Error("an alias over an alternative name was accepted")
	}
	// The alias that was already there still works.
	if _, ok := d.Resolve("sales@hermex.test"); !ok {
		t.Error("the pre-existing alias stopped resolving")
	}
}

// TestAliasInAnUnhostedDomainIsRefused is the sharp one. An alias is reported by
// Identities as an address the account may send as, and both the webmail compose
// gate and the SMTP submission MAIL FROM check honour that list. An alias in a
// domain this server does not host therefore handed the account the right to
// originate mail claiming an address the operator has no authority over, with no
// Sender header to mark it as on-behalf-of.
func TestAliasInAnUnhostedDomainIsRefused(t *testing.T) {
	d := aliasTestDir(t)

	if err := d.CreateAlias("ceo@victim.test", "alice@hermex.test"); !errors.Is(err, ErrAliasNotLocal) {
		t.Fatalf("alias into an unhosted domain = %v, want ErrAliasNotLocal", err)
	}
	ids, err := d.Identities("alice@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ids, "ceo@victim.test") {
		t.Errorf("alice may send as an address in a domain we do not host: %v", ids)
	}
	// A bare value with no domain at all cannot become a route either.
	if err := d.CreateAlias("postmaster", "alice@hermex.test"); !errors.Is(err, ErrAliasNotLocal) {
		t.Errorf("alias with no domain = %v, want ErrAliasNotLocal", err)
	}
}

// TestAliasToAnUnknownAccountIsRefused proves a dead row is refused rather than
// stored. aliases.mainname is matched against users.username, so an alias to
// anything else resolves to nothing and only looks configured.
func TestAliasToAnUnknownAccountIsRefused(t *testing.T) {
	d := aliasTestDir(t)

	if err := d.CreateAlias("ghost@hermex.test", "nobody@hermex.test"); !errors.Is(err, ErrAliasTargetUnknown) {
		t.Fatalf("alias to a missing account = %v, want ErrAliasTargetUnknown", err)
	}
	// An alias may not chain to another alias either.
	if err := d.CreateAlias("sales@hermex.test", "alice@hermex.test"); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateAlias("orders@hermex.test", "sales@hermex.test"); !errors.Is(err, ErrAliasTargetUnknown) {
		t.Errorf("alias chained to another alias = %v, want ErrAliasTargetUnknown", err)
	}
}

// TestValidAliasStillWorks is the guard against overreach: the ordinary case must
// be unaffected, or the check would have replaced one fault with another.
func TestValidAliasStillWorks(t *testing.T) {
	d := aliasTestDir(t)
	if err := d.CreateAlias("sales@hermex.test", "alice@hermex.test"); err != nil {
		t.Fatalf("a legitimate alias was refused: %v", err)
	}
	if _, ok := d.Resolve("sales@hermex.test"); !ok {
		t.Error("the alias does not resolve")
	}
	ids, err := d.Identities("alice@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids, "sales@hermex.test") {
		t.Errorf("alice cannot send as her own alias: %v", ids)
	}
}

// TestSetAliasesForAppliesTheSameChecks proves the domain-scoped admin path is
// covered too. It writes the same rows through its own INSERT, so a check only on
// CreateAlias would leave the surface an administrator actually uses wide open.
func TestSetAliasesForAppliesTheSameChecks(t *testing.T) {
	d := aliasTestDir(t)

	if _, err := d.SetAliasesFor("alice@hermex.test", []string{"bob@hermex.test"}); err == nil {
		t.Error("SetAliasesFor accepted an alias over an existing account")
	}
	if _, err := d.SetAliasesFor("alice@hermex.test", []string{"ceo@victim.test"}); !errors.Is(err, ErrAliasNotLocal) {
		t.Error("SetAliasesFor accepted an alias in a domain we do not host")
	}
	// Neither refusal may have left a partial write behind.
	if ids, _ := d.Identities("alice@hermex.test"); len(ids) != 1 || ids[0] != "alice@hermex.test" {
		t.Errorf("a refused edit left aliases behind: %v", ids)
	}
	if _, ok := d.Resolve("bob@hermex.test"); !ok {
		t.Error("bob stopped resolving after a refused edit")
	}
}

// TestSetAliasesForReplacesItsOwnAliases proves the collision check does not fire
// against the aliases being replaced: re-saving the same list, which is what the
// admin form does on every edit, must keep working.
func TestSetAliasesForReplacesItsOwnAliases(t *testing.T) {
	d := aliasTestDir(t)
	list := []string{"sales@hermex.test", "orders@hermex.test"}
	for range 2 {
		if ok, err := d.SetAliasesFor("alice@hermex.test", list); err != nil || !ok {
			t.Fatalf("saving the same alias list again failed: %v", err)
		}
	}
	ids, err := d.Identities("alice@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if !slices.Contains(ids, a) {
			t.Errorf("%s went missing from %v", a, ids)
		}
	}
}
