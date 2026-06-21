package directory

import (
	"path/filepath"
	"testing"
)

// TestDomainDetailAndCounts proves GetDomain returns a domain's editable fields
// after UpdateDomain writes them, and that the active/inactive/virtual user counts
// reflect the reference split: a normal mailbox is active, a suspended one is
// inactive, and a user with no maildir is virtual.
func TestDomainDetailAndCounts(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()

	id, err := d.CreateDomain("acme.test", filepath.Join(root, "acme.test"))
	if err != nil {
		t.Fatal(err)
	}

	// One user per count bucket.
	if _, err := d.CreateUser("active@acme.test", "pw", filepath.Join(root, "active")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("suspended@acme.test", "pw", filepath.Join(root, "suspended")); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.UpdateUser("suspended@acme.test", UserUpdate{Status: afUserSuspended}); err != nil || !ok {
		t.Fatalf("suspend user = %v, %v", ok, err)
	}
	if _, err := d.CreateUser("virtual@acme.test", "pw", ""); err != nil { // no maildir
		t.Fatal(err)
	}

	if ok, err := d.UpdateDomain(id, DomainUpdate{
		Status: 0, MaxUser: 50, Title: "Acme Inc", Address: "1 Road", AdminName: "Pat", Tel: "555",
	}); err != nil || !ok {
		t.Fatalf("UpdateDomain = %v, %v", ok, err)
	}

	dd, ok, err := d.GetDomain(id)
	if err != nil || !ok {
		t.Fatalf("GetDomain = %v, %v", ok, err)
	}
	if dd.Name != "acme.test" || dd.MaxUser != 50 || dd.Title != "Acme Inc" ||
		dd.Address != "1 Road" || dd.AdminName != "Pat" || dd.Tel != "555" {
		t.Errorf("GetDomain fields = %+v, want the values written by UpdateDomain", dd)
	}
	if dd.ActiveUsers != 1 || dd.InactiveUsers != 1 || dd.VirtualUsers != 1 {
		t.Errorf("counts = active %d / inactive %d / virtual %d, want 1/1/1",
			dd.ActiveUsers, dd.InactiveUsers, dd.VirtualUsers)
	}
}

// TestDomainStatusEnforcement proves suspending a domain via UpdateDomain blocks
// authentication and local delivery through the real authority path (which reads
// domain_status directly), and that reactivating restores both. It tests the
// genuine enforcement points — not a per-user status cascade, which the codebase
// does not use.
func TestDomainStatusEnforcement(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()

	id, err := d.CreateDomain("acme.test", filepath.Join(root, "acme.test"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("alice@acme.test", "pw", filepath.Join(root, "alice")); err != nil {
		t.Fatal(err)
	}

	// Active domain: login and local-delivery both succeed.
	if _, ok := d.Authenticate("alice@acme.test", "pw"); !ok {
		t.Fatal("active domain: Authenticate denied a valid login")
	}
	if local, err := d.IsLocalDomain("acme.test"); err != nil || !local {
		t.Fatalf("active domain: IsLocalDomain = %v, %v, want true", local, err)
	}

	// Suspend: both must be refused.
	if ok, err := d.UpdateDomain(id, DomainUpdate{Status: 1}); err != nil || !ok {
		t.Fatalf("suspend domain = %v, %v", ok, err)
	}
	if _, ok := d.Authenticate("alice@acme.test", "pw"); ok {
		t.Error("suspended domain: Authenticate admitted a login")
	}
	if local, err := d.IsLocalDomain("acme.test"); err != nil || local {
		t.Errorf("suspended domain: IsLocalDomain = %v, %v, want false", local, err)
	}

	// Reactivate: both restored.
	if ok, err := d.UpdateDomain(id, DomainUpdate{Status: 0}); err != nil || !ok {
		t.Fatalf("reactivate domain = %v, %v", ok, err)
	}
	if _, ok := d.Authenticate("alice@acme.test", "pw"); !ok {
		t.Error("reactivated domain: Authenticate denied a valid login")
	}
	if local, err := d.IsLocalDomain("acme.test"); err != nil || !local {
		t.Errorf("reactivated domain: IsLocalDomain = %v, %v, want true", local, err)
	}
}

// TestGetUpdateDomainUnknown proves an unknown domain id is reported as not found
// rather than as an error or a phantom success.
func TestGetUpdateDomainUnknown(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	if _, ok, err := d.GetDomain(999999); err != nil || ok {
		t.Errorf("GetDomain(unknown) = ok %v, err %v, want false/nil", ok, err)
	}
	if ok, err := d.UpdateDomain(999999, DomainUpdate{}); err != nil || ok {
		t.Errorf("UpdateDomain(unknown) = ok %v, err %v, want false/nil", ok, err)
	}
}
