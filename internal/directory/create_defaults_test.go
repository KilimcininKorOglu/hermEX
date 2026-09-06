package directory

import "testing"

// TestCreateDefaultsRoundTrip proves a scope's defaults store and read back, and
// that a per-domain scope is independent of the system scope.
func TestCreateDefaultsRoundTrip(t *testing.T) {
	d, _ := freshDirectory(t)

	_, ok, err := d.GetCreateDefaults(0)
	mustNoErr(t, "read the unset system scope", err)
	wantEq(t, "a scope with nothing stored is found", ok, false)

	mustNoErr(t, "set the system scope", d.SetCreateDefaults(0, CreateDefaults{
		Domain: DomainCreateDefaults{MaxUser: 50},
		User:   UserCreateDefaults{Lang: new("tr"), Web: new(false), StorageKB: new(int64(1024))},
	}))
	got := mustCreateDefaults(t, d, 0)
	wantEq(t, "stored max user", got.Domain.MaxUser, int64(50))
	wantSet(t, "stored lang", got.User.Lang, "tr")
	wantSet(t, "stored web flag", got.User.Web, false)

	// A per-domain scope is stored independently.
	mustNoErr(t, "set the domain scope",
		d.SetCreateDefaults(5, CreateDefaults{User: UserCreateDefaults{EAS: new(false)}}))
	wantSet(t, "the domain scope's EAS flag", mustCreateDefaults(t, d, 5).User.EAS, false)
	// System scope unaffected.
	if mustCreateDefaults(t, d, 0).User.EAS != nil {
		t.Error("the system scope leaked the per-domain EAS override")
	}
}

// mustCreateDefaults reads one scope's stored defaults, requiring the row to
// exist.
func mustCreateDefaults(t *testing.T, d *SQLDirectory, scopeID int64) CreateDefaults {
	t.Helper()
	got, ok, err := d.GetCreateDefaults(scopeID)
	mustNoErr(t, "get create defaults", err)
	if !ok {
		t.Fatalf("no create defaults stored for scope %d", scopeID)
	}
	return got
}

// wantSet checks an optional field carries the expected value. A nil pointer
// means the layer says nothing about it, which is not the same as a zero value.
func wantSet[T comparable](t *testing.T, label string, got *T, want T) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is unset, want %v", label, want)
	}
	wantEq(t, label, *got, want)
}

// TestPurgeDomainClearsCreateDefaults proves a domain purge removes its per-domain
// create-defaults override row.
func TestPurgeDomainClearsCreateDefaults(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	id, err := d.CreateDomain("acme.test", t.TempDir()+"/acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetCreateDefaults(id, CreateDefaults{User: UserCreateDefaults{Web: new(false)}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.GetCreateDefaults(id); !ok {
		t.Fatal("override not stored")
	}
	if ok, err := d.PurgeDomain(id, false); err != nil || !ok {
		t.Fatalf("PurgeDomain = %v, %v", ok, err)
	}
	if _, ok, _ := d.GetCreateDefaults(id); ok {
		t.Error("create-defaults override survived the domain purge")
	}
}

// TestEffectiveUserDefaults proves the three-layer resolution: the built-in
// baseline, the system layer over it, and the per-domain override on top, merged
// field by field. It also proves clearing a domain override falls back to system.
func TestEffectiveUserDefaults(t *testing.T) {
	d, _ := freshDirectory(t)

	// Nothing stored: the built-in baseline (the unconfigured-create behaviour).
	base := mustEffectiveDefaults(t, d, 5)
	wantEq(t, "baseline POP3IMAP", base.POP3IMAP, true)
	wantEq(t, "baseline SMTP", base.SMTP, true)
	wantEq(t, "baseline Web", base.Web, true)
	wantEq(t, "baseline EAS", base.EAS, true)
	wantEq(t, "baseline DAV", base.DAV, true)
	wantEq(t, "baseline ChgPasswd", base.ChgPasswd, false)
	wantEq(t, "baseline lang", base.Lang, "")

	// System layer turns Web off, sets lang and a storage quota.
	mustNoErr(t, "set the system layer", d.SetCreateDefaults(0, CreateDefaults{
		User: UserCreateDefaults{Lang: new("tr"), Web: new(false), StorageKB: new(int64(2048))},
	}))
	sys := mustEffectiveDefaults(t, d, 0)
	wantEq(t, "system-effective Web", sys.Web, false)
	wantEq(t, "system-effective lang", sys.Lang, "tr")
	wantEq(t, "system-effective storage", sys.StorageKB, int64(2048))
	wantEq(t, "system-effective EAS (untouched by the layer)", sys.EAS, true)

	// Domain 5 re-enables Web and turns EAS off; lang/quota inherit from system.
	mustNoErr(t, "set the domain layer", d.SetCreateDefaults(5, CreateDefaults{
		User: UserCreateDefaults{Web: new(true), EAS: new(false)},
	}))
	eff := mustEffectiveDefaults(t, d, 5)
	wantEq(t, "domain-effective Web (domain layer)", eff.Web, true)
	wantEq(t, "domain-effective EAS (domain layer)", eff.EAS, false)
	wantEq(t, "domain-effective lang (inherited)", eff.Lang, "tr")
	wantEq(t, "domain-effective storage (inherited)", eff.StorageKB, int64(2048))

	// Clearing the domain override falls back to the system layer (Web off again).
	ok, err := d.DeleteCreateDefaults(5)
	mustNoErr(t, "delete the domain layer", err)
	wantEq(t, "DeleteCreateDefaults found the layer", ok, true)
	back := mustEffectiveDefaults(t, d, 5)
	wantEq(t, "Web after clearing the override (system layer)", back.Web, false)
	wantEq(t, "EAS after clearing the override (system layer)", back.EAS, true)
}

// mustEffectiveDefaults resolves the create defaults for one domain scope.
func mustEffectiveDefaults(t *testing.T, d *SQLDirectory, domainID int64) ResolvedUserDefaults {
	t.Helper()
	got, err := d.EffectiveUserDefaults(domainID)
	mustNoErr(t, "resolve user defaults", err)
	return got
}
