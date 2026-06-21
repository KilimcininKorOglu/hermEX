package directory

import (
	"path/filepath"
	"testing"

	"hermex/internal/easpolicy"
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

// TestCreateUserMaxUser proves the domain mailbox cap is enforced at user
// creation: max_user 0 is unlimited (the default, so existing domains are not
// suddenly closed), a positive cap rejects creation once reached, and raising or
// clearing the cap reopens creation.
func TestCreateUserMaxUser(t *testing.T) {
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

	// Default max_user 0 means unlimited — creation is not blocked.
	if _, err := d.CreateUser("u1@acme.test", "pw", filepath.Join(root, "u1")); err != nil {
		t.Fatalf("max_user 0 (unlimited) blocked a create: %v", err)
	}

	// Cap at 2: one more is allowed (count 1 < 2), then the next is refused.
	if ok, err := d.UpdateDomain(id, DomainUpdate{MaxUser: 2}); err != nil || !ok {
		t.Fatalf("set max_user = %v, %v", ok, err)
	}
	if _, err := d.CreateUser("u2@acme.test", "pw", filepath.Join(root, "u2")); err != nil {
		t.Fatalf("create within cap blocked: %v", err)
	}
	if _, err := d.CreateUser("u3@acme.test", "pw", filepath.Join(root, "u3")); err == nil {
		t.Error("create over the cap succeeded, want the limit error")
	}

	// Clearing the cap reopens creation.
	if ok, err := d.UpdateDomain(id, DomainUpdate{MaxUser: 0}); err != nil || !ok {
		t.Fatalf("clear max_user = %v, %v", ok, err)
	}
	if _, err := d.CreateUser("u3@acme.test", "pw", filepath.Join(root, "u3")); err != nil {
		t.Errorf("create after clearing the cap blocked: %v", err)
	}
}

// TestSchemaUpgradeAddsDomainColumns proves the idempotent ALTERs actually upgrade
// a pre-existing domains table that lacks the new columns — the path no fresh-DB
// test exercises (CREATE TABLE already carries them there). It drops the added
// columns to simulate an old database, re-runs EnsureSchema, then confirms the
// columns are back by driving the operations that read them: CreateUser selects
// max_user (so every user creation depends on this upgrade), GetDomain selects all
// of them, and GetDomainSyncPolicy selects sync_policy.
func TestSchemaUpgradeAddsDomainColumns(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	// Simulate a database created before the columns existed.
	for _, col := range []string{"max_user", "title", "address", "admin_name", "tel", "sync_policy"} {
		if _, err := db.Exec("ALTER TABLE domains DROP COLUMN IF EXISTS " + col); err != nil {
			t.Fatalf("drop column %s: %v", col, err)
		}
	}

	// The upgrade must re-add every column.
	if err := d.EnsureSchema(); err != nil {
		t.Fatalf("upgrade EnsureSchema: %v", err)
	}

	root := t.TempDir()
	id, err := d.CreateDomain("acme.test", filepath.Join(root, "acme.test"))
	if err != nil {
		t.Fatal(err)
	}
	// CreateUser reads max_user — this fails outright if the column was not re-added.
	if _, err := d.CreateUser("u@acme.test", "pw", filepath.Join(root, "u")); err != nil {
		t.Fatalf("CreateUser after upgrade: %v", err)
	}
	if ok, err := d.UpdateDomain(id, DomainUpdate{MaxUser: 5, Title: "Acme"}); err != nil || !ok {
		t.Fatalf("UpdateDomain after upgrade = %v, %v", ok, err)
	}
	if dd, ok, err := d.GetDomain(id); err != nil || !ok || dd.MaxUser != 5 || dd.Title != "Acme" {
		t.Fatalf("GetDomain after upgrade = %+v, ok %v, err %v", dd, ok, err)
	}
	if ok, err := d.SetDomainSyncPolicy("acme.test", easpolicy.Policy{"DevicePasswordEnabled": 1}); err != nil || !ok {
		t.Fatalf("SetDomainSyncPolicy after upgrade = %v, %v", ok, err)
	}
}

// TestDomainSyncPolicyRoundTrip proves a domain's device-policy override round-trips
// by domain name, that an empty policy clears it, and that an unknown domain is
// reported as not found.
func TestDomainSyncPolicyRoundTrip(t *testing.T) {
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

	// No override yet.
	if p, err := d.GetDomainSyncPolicy("acme.test"); err != nil || p != nil {
		t.Fatalf("initial GetDomainSyncPolicy = %v, %v, want nil/nil", p, err)
	}

	// Set, read back.
	if ok, err := d.SetDomainSyncPolicy("acme.test", easpolicy.Policy{"DevicePasswordEnabled": 1}); err != nil || !ok {
		t.Fatalf("SetDomainSyncPolicy = %v, %v", ok, err)
	}
	got, err := d.GetDomainSyncPolicy("acme.test")
	if err != nil || got["DevicePasswordEnabled"] != 1 {
		t.Fatalf("GetDomainSyncPolicy = %v, %v, want the stored field", got, err)
	}

	// Clearing removes the override.
	if ok, err := d.SetDomainSyncPolicy("acme.test", easpolicy.Policy{}); err != nil || !ok {
		t.Fatalf("clear = %v, %v", ok, err)
	}
	if p, err := d.GetDomainSyncPolicy("acme.test"); err != nil || p != nil {
		t.Errorf("after clear GetDomainSyncPolicy = %v, %v, want nil/nil", p, err)
	}

	// Unknown domain.
	if ok, err := d.SetDomainSyncPolicy("ghost.test", easpolicy.Policy{"DevicePasswordEnabled": 1}); err != nil || ok {
		t.Errorf("SetDomainSyncPolicy(unknown) = %v, %v, want false/nil", ok, err)
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
