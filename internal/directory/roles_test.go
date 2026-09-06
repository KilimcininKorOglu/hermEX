package directory

import (
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
	d, db := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "acme.test")
	alice := mustCreateUser(t, d, root, "alice@acme.test", "pw")
	bob := mustCreateUser(t, d, root, "bob@acme.test", "pw")

	wantRoleNameRules(t, d)

	perms := []Permission{
		{Name: PermSystemAdminRO},
		{Name: PermDomainAdmin, Params: "*"},
		{Name: PermResetPasswd},
	}
	id, err := d.CreateRole("Helpdesk", "Read-only plus password reset", perms, []int64{alice})
	mustNoErr(t, "create role", err)
	wantNonZeroID(t, "role id", id)
	_, err = d.CreateRole("Helpdesk", "", nil, nil)
	wantErr(t, "duplicate role name accepted", err)

	got := mustGetRole(t, d, id)
	wantEq(t, "role name", got.Name, "Helpdesk")
	wantEq(t, "role description", got.Description, "Read-only plus password reset")
	wantEq(t, "permission count", got.PermCount, 3)
	wantEq(t, "permissions stored", len(got.Permissions), 3)
	wantPermissions(t, got.Permissions, perms)
	wantEq(t, "assigned user count", got.UserCount, 1)
	wantOneID(t, "assigned users", got.UserIDs, alice)

	roles, err := d.ListRoles()
	mustNoErr(t, "list roles", err)
	if len(roles) != 1 {
		t.Fatalf("ListRoles = %+v, want one role", roles)
	}
	wantEq(t, "listed user count", roles[0].UserCount, 1)
	wantEq(t, "listed permission count", roles[0].PermCount, 3)

	// Update replaces both sets wholesale: a different permission, a different user.
	newPerms := []Permission{{Name: PermDomainAdminRO, Params: "7"}}
	ok, err := d.UpdateRole(id, "Helpdesk RO", "now read-only", newPerms, []int64{bob})
	mustNoErr(t, "update role", err)
	wantEq(t, "UpdateRole reported the role exists", ok, true)
	got = mustGetRole(t, d, id)
	wantEq(t, "name after update", got.Name, "Helpdesk RO")
	wantEq(t, "permission count after update", got.PermCount, 1)
	wantPermissions(t, got.Permissions, newPerms)
	wantEq(t, "user count after update", got.UserCount, 1)
	wantOneID(t, "users after update", got.UserIDs, bob)
	unknown, _ := d.UpdateRole(999999, "x", "", nil, nil)
	wantEq(t, "UpdateRole on an unknown id", unknown, false)

	deleted, err := d.DeleteRole(id)
	mustNoErr(t, "delete role", err)
	wantEq(t, "DeleteRole reported the role existed", deleted, true)
	_, stillThere, _ := d.GetRole(id)
	wantEq(t, "role present after delete", stillThere, false)
	// Assignment rows cascade with the role.
	wantRows(t, db, "user_roles rows after the role delete (cascade)", 0,
		`SELECT COUNT(*) FROM user_roles WHERE role_id = ?`, id)
	missing, err := d.DeleteRole(999999)
	mustNoErr(t, "delete an unknown role", err)
	wantEq(t, "DeleteRole(unknown)", missing, false)
}

// wantRoleNameRules proves the create path refuses a nameless role, an
// over-length name, and an unknown permission.
func wantRoleNameRules(t *testing.T, d *SQLDirectory) {
	t.Helper()
	_, err := d.CreateRole("", "x", nil, nil)
	wantErr(t, "empty role name accepted", err)
	_, err = d.CreateRole(strings.Repeat("a", 65), "x", nil, nil)
	wantErr(t, "65-character role name accepted (the limit is 64)", err)
	_, err = d.CreateRole("Bad", "x", []Permission{{Name: "Nonsense"}}, nil)
	wantErr(t, "role with an unknown permission accepted", err)
}

// mustGetRole reads a role back, requiring it to exist.
func mustGetRole(t *testing.T, d *SQLDirectory, id int64) RoleDetail {
	t.Helper()
	got, ok, err := d.GetRole(id)
	mustNoErr(t, "get role", err)
	if !ok {
		t.Fatalf("role %d not found", id)
	}
	return got
}

// wantPermissions checks every expected (name, params) pair round-tripped.
func wantPermissions(t *testing.T, got, want []Permission) {
	t.Helper()
	for _, w := range want {
		if !hasPerm(got, w.Name, w.Params) {
			t.Errorf("permission %+v did not round-trip: %+v", w, got)
		}
	}
}

// wantOneID checks an id list holds exactly the one id.
func wantOneID(t *testing.T, label string, got []int64, want int64) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("%s = %v, want [%d]", label, got, want)
	}
	wantEq(t, label, got[0], want)
}

// wantNonZeroID fails when the database issued no id.
func wantNonZeroID(t *testing.T, label string, id int64) {
	t.Helper()
	if id == 0 {
		t.Fatalf("%s is 0; no row was inserted", label)
	}
}

// TestEffectivePermissionsUnionBridge is the no-lockout guarantee: the resolver
// must keep honoring a user's direct admin_roles grants (mapped to their
// permission equivalents) once the named-role model is live, and must union
// those with any named-role permissions without duplicating an overlap. Without
// this bridge an existing, possibly sole, admin loses access the moment the
// resolver governs a real check.
func TestEffectivePermissionsUnionBridge(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "acme.test")
	mk := func(login string) int64 {
		t.Helper()
		return mustCreateUser(t, d, root, login+"@acme.test", "pw")
	}

	// A user with ONLY a legacy direct grant must still resolve to authority.
	legacyOnly := mk("legacy")
	mustNoErr(t, "grant system admin", d.GrantAdminRole(legacyOnly, AdminSystem, 0))
	perms := mustEffectivePermissions(t, d, legacyOnly)
	if !hasPerm(perms, PermSystemAdmin, "") {
		t.Fatalf("legacy system admin lost authority under the resolver: %+v", perms)
	}

	// Org and domain grants map to their scoped permission equivalents.
	scoped := mk("scoped")
	mustNoErr(t, "grant org admin", d.GrantAdminRole(scoped, AdminOrg, 5))
	mustNoErr(t, "grant domain admin", d.GrantAdminRole(scoped, AdminDomain, 7))
	perms = mustEffectivePermissions(t, d, scoped)
	wantPermissions(t, perms, []Permission{
		{Name: PermOrgAdmin, Params: "5"},
		{Name: PermDomainAdmin, Params: "7"},
	})

	// A named role's permissions surface, and overlapping the legacy bridge does
	// not duplicate the permission.
	both := mk("both")
	mustNoErr(t, "grant org admin", d.GrantAdminRole(both, AdminOrg, 9))
	_, err := d.CreateRole("Extra", "",
		[]Permission{
			{Name: PermResetPasswd},
			{Name: PermOrgAdmin, Params: "9"}, // duplicates the legacy org grant scope 9
		},
		[]int64{both})
	mustNoErr(t, "create role", err)
	perms = mustEffectivePermissions(t, d, both)
	wantPermissions(t, perms, []Permission{{Name: PermResetPasswd}})
	wantEq(t, "OrgAdmin(9) occurrences (the union must dedupe the role/legacy overlap)",
		countPerm(perms, PermOrgAdmin, "9"), 1)

	// Cleanup-only sanity: a user with no grants and no roles resolves empty.
	wantEq(t, "permissions of a user with no authority", len(mustEffectivePermissions(t, d, mk("none"))), 0)
}

// mustEffectivePermissions resolves a user's whole permission set.
func mustEffectivePermissions(t *testing.T, d *SQLDirectory, userID int64) []Permission {
	t.Helper()
	perms, err := d.EffectivePermissions(userID)
	mustNoErr(t, "effective permissions", err)
	return perms
}

// countPerm counts how many times an exact (name, params) pair appears.
func countPerm(perms []Permission, name, params string) int {
	n := 0
	for _, p := range perms {
		if p.Name == name && p.Params == params {
			n++
		}
	}
	return n
}
