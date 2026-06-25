package directory

import (
	"path/filepath"
	"testing"
)

// TestDomainBrandingRoundtrip proves a domain's login branding stores and reads back
// per domain, that an unset domain reports no branding (so the caller serves the
// global default), and that clearing every field removes the override rather than
// persisting an empty record.
func TestDomainBrandingRoundtrip(t *testing.T) {
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

	// A fresh domain has no branding and inherits the default.
	if _, has, err := d.GetDomainBranding("hermex.test"); err != nil || has {
		t.Fatalf("fresh domain: has=%v err=%v, want has=false", has, err)
	}

	want := DomainBranding{AppName: "Acme Mail", PrimaryColor: "#ff0000", Tagline: "Mail by Acme"}
	if err := d.SetDomainBranding("hermex.test", want); err != nil {
		t.Fatal(err)
	}
	got, has, err := d.GetDomainBranding("hermex.test")
	if err != nil || !has {
		t.Fatalf("after set: has=%v err=%v, want has=true", has, err)
	}
	if got != want {
		t.Errorf("branding = %+v, want %+v", got, want)
	}

	// Clearing every field removes the override so the domain inherits the default.
	if err := d.SetDomainBranding("hermex.test", DomainBranding{}); err != nil {
		t.Fatal(err)
	}
	if _, has, _ := d.GetDomainBranding("hermex.test"); has {
		t.Error("after clearing all fields, branding should be gone (inherits default)")
	}
}
