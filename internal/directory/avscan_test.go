package directory

import (
	"path/filepath"
	"testing"
)

// TestDomainAVScan proves the per-domain antivirus toggles default off, persist,
// and are read case-insensitively, and an unknown domain reads as off.
func TestDomainAVScan(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if _, err := d.CreateDomain("acme.test", filepath.Join(root, "acme")); err != nil {
		t.Fatal(err)
	}

	if in, out, err := d.GetDomainAVScan("acme.test"); err != nil || in || out {
		t.Fatalf("default = (%v, %v, %v), want (false, false, nil)", in, out, err)
	}

	// Set via a mixed-case name; read via lowercase proves normalization.
	if err := d.SetDomainAVScan("ACME.test", true, false); err != nil {
		t.Fatal(err)
	}
	if in, out, err := d.GetDomainAVScan("acme.test"); err != nil || !in || out {
		t.Fatalf("after set = (%v, %v, %v), want (true, false, nil)", in, out, err)
	}

	if in, out, err := d.GetDomainAVScan("nope.test"); err != nil || in || out {
		t.Fatalf("unknown = (%v, %v, %v), want (false, false, nil)", in, out, err)
	}
}
