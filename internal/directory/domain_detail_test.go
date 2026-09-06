package directory

import (
	"database/sql"
	"path/filepath"
	"testing"

	"hermex/internal/easpolicy"
)

// TestDomainDetailAndCounts proves GetDomain returns a domain's editable fields
// after UpdateDomain writes them, and that the active/inactive/virtual user counts
// reflect the reference split: a normal mailbox is active, a suspended one is
// inactive, and a user with no maildir is virtual.
func TestDomainDetailAndCounts(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	id := mustCreateDomain(t, d, root, "acme.test")

	// One user per count bucket.
	mustCreateUser(t, d, root, "active@acme.test", "pw")
	mustCreateUser(t, d, root, "suspended@acme.test", "pw")
	ok, err := d.UpdateUser("suspended@acme.test", UserUpdate{Status: afUserSuspended})
	mustNoErr(t, "suspend the user", err)
	wantEq(t, "the suspend found the user", ok, true)
	_, err = d.CreateUser("virtual@acme.test", "pw", "") // no maildir
	mustNoErr(t, "create the virtual user", err)

	ok, err = d.UpdateDomain(id, DomainUpdate{
		Status: 0, MaxUser: 50, Title: "Acme Inc", Address: "1 Road", AdminName: "Pat", Tel: "555",
	})
	mustNoErr(t, "update domain", err)
	wantEq(t, "UpdateDomain found the domain", ok, true)

	dd := mustGetDomain(t, d, id)
	wantEq(t, "domain name", dd.Name, "acme.test")
	wantEq(t, "max user", dd.MaxUser, int64(50))
	wantEq(t, "title", dd.Title, "Acme Inc")
	wantEq(t, "address", dd.Address, "1 Road")
	wantEq(t, "admin name", dd.AdminName, "Pat")
	wantEq(t, "telephone", dd.Tel, "555")
	wantEq(t, "active users", dd.ActiveUsers, 1)
	wantEq(t, "inactive users", dd.InactiveUsers, 1)
	wantEq(t, "virtual users", dd.VirtualUsers, 1)
}

// mustGetDomain reads a domain back, requiring it to exist.
func mustGetDomain(t *testing.T, d *SQLDirectory, id int64) DomainDetail {
	t.Helper()
	dd, ok, err := d.GetDomain(id)
	mustNoErr(t, "get domain", err)
	if !ok {
		t.Fatalf("domain %d not found", id)
	}
	return dd
}

// TestDomainStatusEnforcement proves suspending a domain via UpdateDomain blocks
// authentication and local delivery through the real authority path (which reads
// domain_status directly), and that reactivating restores both. It tests the
// genuine enforcement points, not a per-user status cascade, which the codebase
// does not use.
func TestDomainStatusEnforcement(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	id := mustCreateDomain(t, d, root, "acme.test")
	mustCreateUser(t, d, root, "alice@acme.test", "pw")

	// Active domain: login and local-delivery both succeed.
	wantDomainServes(t, d, "active domain", true)

	// Suspend: both must be refused.
	setDomainStatus(t, d, id, 1)
	wantDomainServes(t, d, "suspended domain", false)

	// Reactivate: both restored.
	setDomainStatus(t, d, id, 0)
	wantDomainServes(t, d, "reactivated domain", true)
}

// setDomainStatus writes a domain's status through the admin path.
func setDomainStatus(t *testing.T, d *SQLDirectory, id int64, status int) {
	t.Helper()
	ok, err := d.UpdateDomain(id, DomainUpdate{Status: status})
	mustNoErr(t, "set domain status", err)
	wantEq(t, "UpdateDomain found the domain", ok, true)
}

// wantDomainServes checks both enforcement points, authentication and local
// delivery, agree on whether the domain is serving.
func wantDomainServes(t *testing.T, d *SQLDirectory, what string, serves bool) {
	t.Helper()
	_, authed := d.Authenticate("alice@acme.test", "pw")
	wantEq(t, what+": Authenticate admitted the login", authed, serves)
	local, err := d.IsLocalDomain("acme.test")
	mustNoErr(t, "is local domain", err)
	wantEq(t, what+": IsLocalDomain", local, serves)
}

// TestCreateUserMaxUser proves the domain mailbox cap is enforced at user
// creation: max_user 0 is unlimited (the default, so existing domains are not
// suddenly closed), a positive cap rejects creation once reached, and raising or
// clearing the cap reopens creation.
func TestCreateUserMaxUser(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	id := mustCreateDomain(t, d, root, "acme.test")
	setMaxUser := func(cap int64) {
		t.Helper()
		ok, err := d.UpdateDomain(id, DomainUpdate{MaxUser: cap})
		mustNoErr(t, "set max_user", err)
		wantEq(t, "UpdateDomain found the domain", ok, true)
	}

	// Default max_user 0 means unlimited, creation is not blocked.
	mustCreateUser(t, d, root, "u1@acme.test", "pw")

	// Cap at 2: one more is allowed (count 1 < 2), then the next is refused.
	setMaxUser(2)
	mustCreateUser(t, d, root, "u2@acme.test", "pw")
	_, err := d.CreateUser("u3@acme.test", "pw", filepath.Join(root, "u3"))
	wantErr(t, "a create over the cap succeeded", err)

	// Clearing the cap reopens creation.
	setMaxUser(0)
	mustCreateUser(t, d, root, "u3@acme.test", "pw")
}

// TestSchemaUpgradeAddsDomainColumns proves the idempotent ALTERs actually upgrade
// a pre-existing domains table that lacks the new columns, the path no fresh-DB
// test exercises (CREATE TABLE already carries them there). It drops the added
// columns to simulate an old database, re-runs EnsureSchema, then confirms the
// columns are back by driving the operations that read them: CreateUser selects
// max_user (so every user creation depends on this upgrade), GetDomain selects all
// of them, and GetDomainSyncPolicy selects sync_policy.
func TestSchemaUpgradeAddsDomainColumns(t *testing.T) {
	d, db := freshDirectory(t)
	dropDomainColumns(t, db)

	// The upgrade must re-add every column.
	mustNoErr(t, "upgrade EnsureSchema", d.EnsureSchema())

	root := t.TempDir()
	id := mustCreateDomain(t, d, root, "acme.test")
	// CreateUser reads max_user, this fails outright if the column was not re-added.
	mustCreateUser(t, d, root, "u@acme.test", "pw")
	ok, err := d.UpdateDomain(id, DomainUpdate{MaxUser: 5, Title: "Acme"})
	mustNoErr(t, "update domain after the upgrade", err)
	wantEq(t, "UpdateDomain found the domain", ok, true)
	dd := mustGetDomain(t, d, id)
	wantEq(t, "max user after the upgrade", dd.MaxUser, int64(5))
	wantEq(t, "title after the upgrade", dd.Title, "Acme")
	ok, err = d.SetDomainSyncPolicy("acme.test", easpolicy.Policy{"DevicePasswordEnabled": 1})
	mustNoErr(t, "set the sync policy after the upgrade", err)
	wantEq(t, "SetDomainSyncPolicy found the domain", ok, true)
}

// dropDomainColumns simulates a database created before the columns existed.
//
// The drop rebuilds the table (ALGORITHM=COPY) instead of MariaDB's default
// instant DROP: an instant drop leaves per-column metadata that the matching
// instant re-add (the EnsureSchema that follows) compounds, and against the
// persistent test database this accumulates across runs until it crosses the
// InnoDB row-size limit and every EnsureSchema fails with "Row size too large".
// A copy rebuild leaves the table clean each run, and also more faithfully
// mirrors an old database (built before instant DDL existed).
func dropDomainColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, col := range []string{"max_user", "title", "address", "admin_name", "tel", "sync_policy"} {
		_, err := db.Exec("ALTER TABLE domains DROP COLUMN IF EXISTS " + col + ", ALGORITHM=COPY")
		mustNoErr(t, "drop column "+col, err)
	}
	// Also clear the migration bookkeeping so the runner re-applies v1 (the
	// idempotent baseline) instead of seeing the database as already current,
	// the realistic adoption path for a database that predates migrations.
	_, err := db.Exec("DELETE FROM schema_migrations")
	mustNoErr(t, "reset the migration bookkeeping", err)
}

// TestSchemaBaselineAdoption proves adopting a pre-migration database, every
// table already present with data, but no schema_migrations bookkeeping, is a
// clean no-op: the baseline is recorded as v1 and existing data is untouched. It
// is the safety proof that turning on the migration runner cannot disturb a
// deployed directory.
func TestSchemaBaselineAdoption(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()
	id, err := d.CreateDomain("base.test", filepath.Join(root, "base.test"))
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a database that predates migration bookkeeping: all tables and data
	// present, but no record of which version it is at.
	if _, err := db.Exec("DROP TABLE IF EXISTS schema_migrations"); err != nil {
		t.Fatalf("drop bookkeeping: %v", err)
	}

	// Adoption must be a clean no-op that records the baseline.
	if err := d.EnsureSchema(); err != nil {
		t.Fatalf("baseline adoption: %v", err)
	}
	var ver int
	want := directoryMigrations[len(directoryMigrations)-1].Version // the latest migration
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&ver); err != nil || ver != want {
		t.Fatalf("recorded version = %d (err %v), want %d (the latest migration)", ver, err, want)
	}
	// The existing domain, and so all data, survived the adoption.
	if dd, ok, err := d.GetDomain(id); err != nil || !ok || dd.Name != "base.test" {
		t.Fatalf("domain lost across adoption: %+v, ok %v, err %v", dd, ok, err)
	}
}

// TestDomainSyncPolicyRoundTrip proves a domain's device-policy override round-trips
// by domain name, that an empty policy clears it, and that an unknown domain is
// reported as not found.
func TestDomainSyncPolicyRoundTrip(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "acme.test")
	readPolicy := func() easpolicy.Policy {
		t.Helper()
		p, err := d.GetDomainSyncPolicy("acme.test")
		mustNoErr(t, "get the domain sync policy", err)
		return p
	}

	// No override yet.
	wantEq(t, "policy fields before any override", len(readPolicy()), 0)

	// Set, read back.
	ok, err := d.SetDomainSyncPolicy("acme.test", easpolicy.Policy{"DevicePasswordEnabled": 1})
	mustNoErr(t, "set the domain sync policy", err)
	wantEq(t, "SetDomainSyncPolicy found the domain", ok, true)
	wantEq(t, "the stored policy field", readPolicy()["DevicePasswordEnabled"], 1)

	// Clearing removes the override.
	ok, err = d.SetDomainSyncPolicy("acme.test", easpolicy.Policy{})
	mustNoErr(t, "clear the domain sync policy", err)
	wantEq(t, "the clear found the domain", ok, true)
	wantEq(t, "policy fields after clearing", len(readPolicy()), 0)

	// Unknown domain.
	ghost, err := d.SetDomainSyncPolicy("ghost.test", easpolicy.Policy{"DevicePasswordEnabled": 1})
	mustNoErr(t, "set a policy on an unknown domain", err)
	wantEq(t, "SetDomainSyncPolicy(unknown) found a domain", ghost, false)
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
