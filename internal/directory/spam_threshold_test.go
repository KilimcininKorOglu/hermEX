package directory

import (
	"path/filepath"
	"testing"
)

func setupSpamThreshold(t *testing.T) (*SQLDirectory, string) {
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
	maildir := filepath.Join(root, "users", "alice")
	if _, err := d.CreateUser("alice@hermex.test", "secret", maildir); err != nil {
		t.Fatal(err)
	}
	return d, maildir
}

// TestSpamThresholdResolution proves the per-recipient threshold resolves user
// override → domain override → none (so the caller inherits the global threshold),
// keyed by maildir.
func TestSpamThresholdResolution(t *testing.T) {
	d, maildir := setupSpamThreshold(t)

	// No overrides → not found (the caller uses the global threshold).
	if _, ok, err := d.SpamThresholdForMaildir(maildir); err != nil || ok {
		t.Fatalf("fresh user resolves to ok=%v err=%v, want no override", ok, err)
	}

	// A domain override applies when the user has none.
	dom := 12
	if err := d.SetDomainSpamThreshold("hermex.test", &dom); err != nil {
		t.Fatal(err)
	}
	if th, ok, err := d.SpamThresholdForMaildir(maildir); err != nil || !ok || th != 12 {
		t.Fatalf("domain override = (%d, %v, %v), want (12, true, nil)", th, ok, err)
	}

	// A user override beats the domain override.
	usr := 4
	if err := d.SetUserSpamThreshold("alice@hermex.test", &usr); err != nil {
		t.Fatal(err)
	}
	if th, ok, err := d.SpamThresholdForMaildir(maildir); err != nil || !ok || th != 4 {
		t.Fatalf("user override = (%d, %v, %v), want (4, true, nil)", th, ok, err)
	}

	// Clearing the user override falls back to the domain override.
	if err := d.SetUserSpamThreshold("alice@hermex.test", nil); err != nil {
		t.Fatal(err)
	}
	if th, ok, _ := d.SpamThresholdForMaildir(maildir); !ok || th != 12 {
		t.Errorf("after clearing user override = (%d, %v), want (12, true)", th, ok)
	}
}

// TestSpamThresholdGetReflectsSet proves the per-scope getters read back what was set
// and report nil (inherit) once cleared.
func TestSpamThresholdGetReflectsSet(t *testing.T) {
	d, _ := setupSpamThreshold(t)

	if v, err := d.GetUserSpamThreshold("alice@hermex.test"); err != nil || v != nil {
		t.Fatalf("fresh user threshold = %v err %v, want nil", v, err)
	}
	n := 7
	if err := d.SetUserSpamThreshold("alice@hermex.test", &n); err != nil {
		t.Fatal(err)
	}
	if v, err := d.GetUserSpamThreshold("alice@hermex.test"); err != nil || v == nil || *v != 7 {
		t.Fatalf("user threshold after set = %v err %v, want 7", v, err)
	}
	if err := d.SetUserSpamThreshold("alice@hermex.test", nil); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetUserSpamThreshold("alice@hermex.test"); v != nil {
		t.Errorf("user threshold after clear = %v, want nil", v)
	}

	dn := 15
	if err := d.SetDomainSpamThreshold("hermex.test", &dn); err != nil {
		t.Fatal(err)
	}
	if v, err := d.GetDomainSpamThreshold("hermex.test"); err != nil || v == nil || *v != 15 {
		t.Fatalf("domain threshold after set = %v err %v, want 15", v, err)
	}
}
