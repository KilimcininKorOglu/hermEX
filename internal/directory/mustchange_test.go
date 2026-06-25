package directory

import (
	"path/filepath"
	"testing"
)

// TestRequirePasswordChange proves the must-change-password flag set by an admin
// reset round-trips through GetUser, and that the user clears it by changing their
// own password. A fresh account does not require a change; this is what gates the
// webmail forced-change screen.
func TestRequirePasswordChange(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()

	if _, err := d.CreateDomain("acme.test", filepath.Join(root, "acme.test")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("u@acme.test", "pw", filepath.Join(root, "u")); err != nil {
		t.Fatal(err)
	}

	// A fresh account does not require a password change.
	if u, ok, err := d.GetUser("u@acme.test"); err != nil || !ok {
		t.Fatalf("GetUser fresh = ok %v, err %v", ok, err)
	} else if u.MustChangePassword {
		t.Fatal("a fresh account must not require a password change")
	}

	// An admin reset sets the flag.
	if ok, err := d.RequirePasswordChange("u@acme.test", true); err != nil || !ok {
		t.Fatalf("RequirePasswordChange(true) = ok %v, err %v", ok, err)
	}
	if u, _, _ := d.GetUser("u@acme.test"); !u.MustChangePassword {
		t.Error("must_change_password should be true after an admin reset")
	}

	// The user changing their own password clears it.
	if ok, err := d.RequirePasswordChange("u@acme.test", false); err != nil || !ok {
		t.Fatalf("RequirePasswordChange(false) = ok %v, err %v", ok, err)
	}
	if u, _, _ := d.GetUser("u@acme.test"); u.MustChangePassword {
		t.Error("must_change_password should be cleared after the user changes it")
	}
}
