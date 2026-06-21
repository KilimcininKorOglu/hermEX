package directory

import (
	"path/filepath"
	"strings"
	"testing"
)

// hasPerm reports whether a permission set contains an exact (name, params) pair.
func hasPerm(perms []Permission, name, params string) bool {
	for _, p := range perms {
		if p.Name == name && p.Params == params {
			return true
		}
	}
	return false
}

// TestRolePermissionValidation pins the scoping rules: scoped permissions need a
// "*" or decimal-id parameter, unscoped permissions must carry none, and an
// unknown name is rejected. It is a pure unit test (no database).
func TestRolePermissionValidation(t *testing.T) {
	ok := []Permission{
		{Name: PermSystemAdmin},
		{Name: PermSystemAdminRO},
		{Name: PermDomainPurge},
		{Name: PermResetPasswd},
		{Name: PermOrgAdmin, Params: "*"},
		{Name: PermOrgAdmin, Params: "12"},
		{Name: PermDomainAdmin, Params: "5"},
		{Name: PermDomainAdminRO, Params: "*"},
	}
	for _, p := range ok {
		if err := validatePermission(p); err != nil {
			t.Errorf("validatePermission(%+v) = %v, want nil", p, err)
		}
	}
	bad := []Permission{
		{Name: "Nonsense"},                           // unknown name
		{Name: PermSystemAdmin, Params: "1"},         // unscoped name with a scope
		{Name: PermDomainPurge, Params: "*"},         // unscoped name with a scope
		{Name: PermOrgAdmin},                         // scoped name without a scope
		{Name: PermDomainAdmin, Params: "not-an-id"}, // scope is neither * nor a number
		{Name: PermDomainAdminRO, Params: ""},        // scoped name without a scope
	}
	for _, p := range bad {
		if err := validatePermission(p); err == nil {
			t.Errorf("validatePermission(%+v) = nil, want error", p)
		}
	}
}

// TestRoleCRUD proves named-role create/get/list/update/delete: name validation
// and uniqueness, the permission set and user assignments round-trip, update
// replaces both sets wholesale, and delete cascades the assignment rows.
func TestRoleCRUD(t *testing.T) {
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
	alice, err := d.CreateUser("alice@acme.test", "pw", filepath.Join(root, "alice"))
	if err != nil {
		t.Fatal(err)
	}
	bob, err := d.CreateUser("bob@acme.test", "pw", filepath.Join(root, "bob"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.CreateRole("", "x", nil, nil); err == nil {
		t.Error("empty role name accepted")
	}
	if _, err := d.CreateRole(strings.Repeat("a", 65), "x", nil, nil); err == nil {
		t.Error("65-character role name accepted (limit is 64)")
	}
	if _, err := d.CreateRole("Bad", "x", []Permission{{Name: "Nonsense"}}, nil); err == nil {
		t.Error("role with an unknown permission accepted")
	}

	perms := []Permission{
		{Name: PermSystemAdminRO},
		{Name: PermDomainAdmin, Params: "*"},
		{Name: PermResetPasswd},
	}
	id, err := d.CreateRole("Helpdesk", "Read-only plus password reset", perms, []int64{alice})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("role id 0 issued")
	}
	if _, err := d.CreateRole("Helpdesk", "", nil, nil); err == nil {
		t.Error("duplicate role name accepted")
	}

	got, ok, err := d.GetRole(id)
	if err != nil || !ok {
		t.Fatalf("GetRole = %+v, %v, %v", got, ok, err)
	}
	if got.Name != "Helpdesk" || got.Description != "Read-only plus password reset" {
		t.Errorf("role identity = %+v", got.RoleInfo)
	}
	if got.PermCount != 3 || len(got.Permissions) != 3 {
		t.Errorf("permission count = %d, want 3", got.PermCount)
	}
	if !hasPerm(got.Permissions, PermSystemAdminRO, "") ||
		!hasPerm(got.Permissions, PermDomainAdmin, "*") ||
		!hasPerm(got.Permissions, PermResetPasswd, "") {
		t.Errorf("permissions did not round-trip: %+v", got.Permissions)
	}
	if got.UserCount != 1 || len(got.UserIDs) != 1 || got.UserIDs[0] != alice {
		t.Errorf("user assignment = %+v, want [%d]", got.UserIDs, alice)
	}

	roles, err := d.ListRoles()
	if err != nil || len(roles) != 1 || roles[0].UserCount != 1 || roles[0].PermCount != 3 {
		t.Fatalf("ListRoles = %+v, %v", roles, err)
	}

	// Update replaces both sets wholesale: a different permission, a different user.
	newPerms := []Permission{{Name: PermDomainAdminRO, Params: "7"}}
	if ok, err := d.UpdateRole(id, "Helpdesk RO", "now read-only", newPerms, []int64{bob}); err != nil || !ok {
		t.Fatalf("UpdateRole = %v, %v", ok, err)
	}
	got, _, _ = d.GetRole(id)
	if got.Name != "Helpdesk RO" || got.PermCount != 1 || !hasPerm(got.Permissions, PermDomainAdminRO, "7") {
		t.Errorf("after update permissions = %+v (name %q)", got.Permissions, got.Name)
	}
	if got.UserCount != 1 || got.UserIDs[0] != bob {
		t.Errorf("after update users = %+v, want [%d]", got.UserIDs, bob)
	}
	if ok, _ := d.UpdateRole(999999, "x", "", nil, nil); ok {
		t.Error("UpdateRole on an unknown id returned ok=true")
	}

	if ok, err := d.DeleteRole(id); err != nil || !ok {
		t.Fatalf("DeleteRole = %v, %v", ok, err)
	}
	if _, ok, _ := d.GetRole(id); ok {
		t.Error("role still present after delete")
	}
	// Assignment rows cascade with the role.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_roles WHERE role_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("user_roles rows = %d after role delete, want 0 (cascade)", n)
	}
	if ok, err := d.DeleteRole(999999); ok || err != nil {
		t.Errorf("DeleteRole(unknown) = %v, %v; want false, nil", ok, err)
	}
}

// TestEffectivePermissionsUnionBridge is the no-lockout guarantee: the resolver
// must keep honoring a user's direct admin_roles grants (mapped to their
// permission equivalents) once the named-role model is live, and must union
// those with any named-role permissions without duplicating an overlap. Without
// this bridge an existing — possibly sole — admin loses access the moment the
// resolver governs a real check.
func TestEffectivePermissionsUnionBridge(t *testing.T) {
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
	mk := func(login string) int64 {
		id, err := d.CreateUser(login+"@acme.test", "pw", filepath.Join(root, login))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	// A user with ONLY a legacy direct grant must still resolve to authority.
	legacyOnly := mk("legacy")
	if err := d.GrantAdminRole(legacyOnly, AdminSystem, 0); err != nil {
		t.Fatal(err)
	}
	perms, err := d.EffectivePermissions(legacyOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPerm(perms, PermSystemAdmin, "") {
		t.Fatalf("legacy system admin lost authority under the resolver: %+v", perms)
	}

	// Org and domain grants map to their scoped permission equivalents.
	scoped := mk("scoped")
	if err := d.GrantAdminRole(scoped, AdminOrg, 5); err != nil {
		t.Fatal(err)
	}
	if err := d.GrantAdminRole(scoped, AdminDomain, 7); err != nil {
		t.Fatal(err)
	}
	perms, _ = d.EffectivePermissions(scoped)
	if !hasPerm(perms, PermOrgAdmin, "5") {
		t.Errorf("org grant scope 5 did not map to OrgAdmin(5): %+v", perms)
	}
	if !hasPerm(perms, PermDomainAdmin, "7") {
		t.Errorf("domain grant scope 7 did not map to DomainAdmin(7): %+v", perms)
	}

	// A named role's permissions surface, and overlapping the legacy bridge does
	// not duplicate the permission.
	both := mk("both")
	if err := d.GrantAdminRole(both, AdminOrg, 9); err != nil {
		t.Fatal(err)
	}
	roleID, err := d.CreateRole("Extra",
		"",
		[]Permission{
			{Name: PermResetPasswd},
			{Name: PermOrgAdmin, Params: "9"}, // duplicates the legacy org grant scope 9
		},
		[]int64{both})
	if err != nil {
		t.Fatal(err)
	}
	perms, _ = d.EffectivePermissions(both)
	if !hasPerm(perms, PermResetPasswd, "") {
		t.Errorf("named-role permission missing: %+v", perms)
	}
	dups := 0
	for _, p := range perms {
		if p.Name == PermOrgAdmin && p.Params == "9" {
			dups++
		}
	}
	if dups != 1 {
		t.Errorf("OrgAdmin(9) appeared %d times, want 1 (union must dedupe the role/legacy overlap)", dups)
	}

	// Cleanup-only sanity: a user with no grants and no roles resolves empty.
	none := mk("none")
	if perms, _ := d.EffectivePermissions(none); len(perms) != 0 {
		t.Errorf("user with no authority resolved %+v, want empty", perms)
	}
	_ = roleID
}
