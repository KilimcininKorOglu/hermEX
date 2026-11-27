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
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	for _, u := range []string{"alice@hermex.test", "bob@hermex.test"} {
		seedUser(t, d, root, u, "secret-pw1")
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
	if path, ok := d.Resolve("bob@hermex.test"); !ok || path == "" {
		t.Errorf("bob no longer resolves (%q, %v), so the collision was written anyway", path, ok)
	}
	if path, ok := d.Authenticate("bob@hermex.test", "secret-pw1"); !ok || path == "" {
		t.Errorf("bob can no longer sign in (%q, %v), so the collision was written anyway", path, ok)
	}
}

// TestAliasOverAnotherAliasIsRefused proves the same protection covers the other
// two resolution keys, which collide exactly the same way.
func TestAliasOverAnotherAliasIsRefused(t *testing.T) {
	d := aliasTestDir(t)
	mustNoErr(t, "create sales alias", d.CreateAlias("sales@hermex.test", "alice@hermex.test"))
	if err := d.CreateAlias("sales@hermex.test", "bob@hermex.test"); err == nil {
		t.Error("an alias over another alias was accepted")
	}
	found, err := d.SetAltnames("bob@hermex.test", []string{"bobby@hermex.test"})
	mustNoErr(t, "set altname", err)
	wantEq(t, "SetAltnames found bob", found, true)
	if err := d.CreateAlias("bobby@hermex.test", "alice@hermex.test"); err == nil {
		t.Error("an alias over an alternative name was accepted")
	}
	// The alias that was already there still works.
	if path, ok := d.Resolve("sales@hermex.test"); !ok || path == "" {
		t.Errorf("the pre-existing alias stopped resolving: %q, %v", path, ok)
	}
}

// TestAltnameOverAnAccountOrAliasIsRefused proves an alternative name follows
// the same collision rule as an alias. An altname equal to another account, to
// the account's own address, or to an alias made the address ambiguous, so bob
// could no longer sign in and the alias stopped delivering.
func TestAltnameOverAnAccountOrAliasIsRefused(t *testing.T) {
	d := aliasTestDir(t)
	mustNoErr(t, "create sales alias", d.CreateAlias("sales@hermex.test", "bob@hermex.test"))
	for _, name := range []string{"bob@hermex.test", "alice@hermex.test", "sales@hermex.test"} {
		found, err := d.SetAltnames("alice@hermex.test", []string{name})
		wantErr(t, "altname "+name+" refused", err)
		wantEq(t, "refused altname found for "+name, found, false)
	}
	path, ok := d.Authenticate("bob@hermex.test", "secret-pw1")
	wantEq(t, "bob sign-in ok", ok, true)
	wantEq(t, "bob sign-in path non-empty", path != "", true)
	path, ok = d.Resolve("sales@hermex.test")
	wantEq(t, "alias resolve ok", ok, true)
	wantEq(t, "alias resolve path non-empty", path != "", true)
	// Re-saving the account's own altnames still works.
	for range 2 {
		found, err := d.SetAltnames("alice@hermex.test", []string{"ali"})
		mustNoErr(t, "re-save altname", err)
		wantEq(t, "re-save altname found", found, true)
	}
}

func aliasCheckCreators(t *testing.T, d *SQLDirectory, root string) map[string]func(addr string) error {
	t.Helper()
	return map[string]func(addr string) error{
		"user": func(a string) error {
			id, err := d.CreateUser(a, "password1", filepath.Join(root, a))
			wantEq(t, "refused CreateUser id", id, int64(0))
			return err
		},
		"room": func(a string) error {
			id, err := d.CreateRoom(a, "R", filepath.Join(root, a), 0, false)
			wantEq(t, "refused CreateRoom id", id, int64(0))
			return err
		},
		"contact": func(a string) error {
			id, err := d.CreateContact(a, "C", "hermex.test")
			wantEq(t, "refused CreateContact id", id, int64(0))
			return err
		},
		"list": func(a string) error {
			id, err := d.CreateMList(a, 0, 0)
			wantEq(t, "refused CreateMList id", id, int64(0))
			return err
		},
		"ldap": func(a string) error {
			created, err := d.UpsertLDAPUser(a, []byte{0x09}, filepath.Join(root, a))
			wantEq(t, "refused UpsertLDAPUser created", created, false)
			return err
		},
	}
}

// TestNewAccountRowsCannotTakeAnAliasOrAltname creates an account, a room, a
// contact, a mailing list and an LDAP-synced account at an address that is
// already an alias or an alternative name. Each would make the address resolve
// twice, so resolve() answers nothing and the existing route stops working.
func TestNewAccountRowsCannotTakeAnAliasOrAltname(t *testing.T) {
	d := aliasTestDir(t)
	creators := aliasCheckCreators(t, d, t.TempDir())
	var altnames []string
	for name := range creators {
		altnames = append(altnames, name+"-alt@hermex.test")
		mustNoErr(t, "seed alias "+name, d.CreateAlias(name+"-alias@hermex.test", "alice@hermex.test"))
	}
	found, err := d.SetAltnames("bob@hermex.test", altnames)
	mustNoErr(t, "seed altnames", err)
	wantEq(t, "SetAltnames found bob", found, true)
	for name, create := range creators {
		for _, addr := range []string{name + "-alias@hermex.test", name + "-alt@hermex.test"} {
			wantErr(t, name+" refused "+addr, create(addr))
		}
		path, ok := d.Resolve(name + "-alias@hermex.test")
		wantEq(t, name+" alias still resolves", ok && path != "", true)
		authPath, ok := d.Authenticate(name+"-alt@hermex.test", "secret-pw1")
		wantEq(t, name+" altname still authenticates", ok && authPath != "", true)
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
	if path, ok := d.Resolve("sales@hermex.test"); !ok || path == "" {
		t.Errorf("the alias does not resolve: %q, %v", path, ok)
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

	if found, err := d.SetAliasesFor("alice@hermex.test", []string{"bob@hermex.test"}); err == nil || found {
		t.Errorf("SetAliasesFor accepted an alias over an existing account (found=%v)", found)
	}
	if found, err := d.SetAliasesFor("alice@hermex.test", []string{"ceo@victim.test"}); !errors.Is(err, ErrAliasNotLocal) || found {
		t.Errorf("SetAliasesFor accepted an alias in a domain we do not host (found=%v, err=%v)", found, err)
	}
	// Neither refusal may have left a partial write behind.
	ids, err := d.Identities("alice@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "alice@hermex.test" {
		t.Errorf("a refused edit left aliases behind: %v", ids)
	}
	if path, ok := d.Resolve("bob@hermex.test"); !ok || path == "" {
		t.Errorf("bob stopped resolving after a refused edit: %q, %v", path, ok)
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
