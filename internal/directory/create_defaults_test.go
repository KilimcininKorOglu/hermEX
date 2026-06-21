package directory

import "testing"

func ptr[T any](v T) *T { return &v }

// TestCreateDefaultsRoundTrip proves a scope's defaults store and read back, and
// that a per-domain scope is independent of the system scope.
func TestCreateDefaultsRoundTrip(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	if _, ok, err := d.GetCreateDefaults(0); err != nil || ok {
		t.Fatalf("initial GetCreateDefaults(0) = ok %v, err %v, want false/nil", ok, err)
	}

	sys := CreateDefaults{
		Domain: DomainCreateDefaults{MaxUser: 50},
		User:   UserCreateDefaults{Lang: ptr("tr"), Web: ptr(false), StorageKB: ptr(int64(1024))},
	}
	if err := d.SetCreateDefaults(0, sys); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.GetCreateDefaults(0)
	if err != nil || !ok {
		t.Fatalf("GetCreateDefaults(0) = ok %v, err %v", ok, err)
	}
	if got.Domain.MaxUser != 50 || got.User.Lang == nil || *got.User.Lang != "tr" ||
		got.User.Web == nil || *got.User.Web != false {
		t.Errorf("round-trip = %+v, want maxUser 50 / lang tr / web false", got)
	}

	// A per-domain scope is stored independently.
	if err := d.SetCreateDefaults(5, CreateDefaults{User: UserCreateDefaults{EAS: ptr(false)}}); err != nil {
		t.Fatal(err)
	}
	if dom, ok, _ := d.GetCreateDefaults(5); !ok || dom.User.EAS == nil || *dom.User.EAS != false {
		t.Errorf("per-domain round-trip = %+v, ok %v, want EAS false", dom, ok)
	}
	// System scope unaffected.
	if sys0, _, _ := d.GetCreateDefaults(0); sys0.User.EAS != nil {
		t.Errorf("system scope leaked the per-domain EAS override")
	}
}

// TestEffectiveUserDefaults proves the three-layer resolution: the built-in
// baseline, the system layer over it, and the per-domain override on top, merged
// field by field. It also proves clearing a domain override falls back to system.
func TestEffectiveUserDefaults(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	// Nothing stored: the built-in baseline (the unconfigured-create behaviour).
	base, err := d.EffectiveUserDefaults(5)
	if err != nil {
		t.Fatal(err)
	}
	if !(base.POP3IMAP && base.SMTP && base.Web && base.EAS && base.DAV) || base.ChgPasswd || base.Lang != "" {
		t.Errorf("baseline = %+v, want POP3IMAP/SMTP/Web/EAS/DAV on, ChgPasswd off, no lang", base)
	}

	// System layer turns Web off, sets lang and a storage quota.
	if err := d.SetCreateDefaults(0, CreateDefaults{
		User: UserCreateDefaults{Lang: ptr("tr"), Web: ptr(false), StorageKB: ptr(int64(2048))},
	}); err != nil {
		t.Fatal(err)
	}
	sys, _ := d.EffectiveUserDefaults(0)
	if sys.Web || sys.Lang != "tr" || sys.StorageKB != 2048 || !sys.EAS {
		t.Errorf("system-effective = %+v, want Web off / lang tr / storage 2048 / EAS still on", sys)
	}

	// Domain 5 re-enables Web and turns EAS off; lang/quota inherit from system.
	if err := d.SetCreateDefaults(5, CreateDefaults{
		User: UserCreateDefaults{Web: ptr(true), EAS: ptr(false)},
	}); err != nil {
		t.Fatal(err)
	}
	eff, _ := d.EffectiveUserDefaults(5)
	if !eff.Web || eff.EAS || eff.Lang != "tr" || eff.StorageKB != 2048 {
		t.Errorf("domain-effective = %+v, want Web on (domain) / EAS off (domain) / lang tr+storage 2048 (system)", eff)
	}

	// Clearing the domain override falls back to the system layer (Web off again).
	if ok, err := d.DeleteCreateDefaults(5); err != nil || !ok {
		t.Fatalf("DeleteCreateDefaults(5) = %v, %v", ok, err)
	}
	back, _ := d.EffectiveUserDefaults(5)
	if back.Web || !back.EAS {
		t.Errorf("after clearing override = %+v, want Web off / EAS on (system layer)", back)
	}
}
