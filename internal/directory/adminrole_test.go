package directory

import (
	"path/filepath"
	"testing"
)

// TestAdminRoles proves a login resolves to its user id, admin roles round-trip
// (grant is idempotent), and an unknown role tier is rejected.
func TestAdminRoles(t *testing.T) {
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
	uid, err := d.CreateUser("admin@hermex.test", "pw", filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}

	id, ok, err := d.UserID("admin@hermex.test")
	if err != nil || !ok || id != uid {
		t.Fatalf("UserID = (%d, %v, %v), want (%d, true, nil)", id, ok, err, uid)
	}
	if _, ok, _ := d.UserID("ghost@hermex.test"); ok {
		t.Error("UserID resolved an unknown login")
	}

	if roles, err := d.AdminRoles(uid); err != nil || len(roles) != 0 {
		t.Fatalf("AdminRoles (fresh) = (%v, %v), want empty", roles, err)
	}

	if err := d.GrantAdminRole(uid, AdminSystem, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.GrantAdminRole(uid, AdminOrg, 5); err != nil {
		t.Fatal(err)
	}
	if err := d.GrantAdminRole(uid, AdminOrg, 5); err != nil { // idempotent
		t.Fatal(err)
	}

	roles, err := d.AdminRoles(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 {
		t.Fatalf("AdminRoles = %v, want 2 (system + org:5)", roles)
	}
	var hasSystem, hasOrg bool
	for _, r := range roles {
		if r.Role == AdminSystem && r.ScopeID == 0 {
			hasSystem = true
		}
		if r.Role == AdminOrg && r.ScopeID == 5 {
			hasOrg = true
		}
	}
	if !hasSystem || !hasOrg {
		t.Errorf("roles = %v, want system(scope 0) + org(scope 5)", roles)
	}

	if err := d.GrantAdminRole(uid, "wizard", 0); err == nil {
		t.Error("GrantAdminRole accepted an unknown role tier")
	}
}
