package directory

import (
	"slices"
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
		{Name: PermDomainAdmin, Params: "0"},         // id 0 names no domain
		{Name: PermOrgAdmin, Params: "-5"},           // a negative id names no organization
		{Name: PermDomainAdmin, Params: "05"},        // the panel matches the decimal form as text
		{Name: PermDomainAdmin, Params: "+5"},        // so another spelling of an id is refused
		{Name: PermDomainAdmin, Params: " 5"},        // with surrounding space too
	}
	for _, p := range bad {
		if err := validatePermission(p); err == nil {
			t.Errorf("validatePermission(%+v) = nil, want error", p)
		}
	}
}

// TestStoredScopeIsMadeCanonical stores scopes written before the validation
// required the decimal form. The migration rewrites "05" and "+7" to "5" and
// "7", drops "005" where "5" is already granted, and leaves "*" alone, so the
// role grants exactly the domains its editor shows.
func TestStoredScopeIsMadeCanonical(t *testing.T) {
	d, db := freshDirectory(t)
	id, err := d.CreateRole("Legacy", "", []Permission{{Name: PermDomainAdmin, Params: "*"}}, nil)
	mustNoErr(t, "create role", err)
	for _, p := range []string{"05", "005", "+7"} {
		res, err := db.Exec(`INSERT INTO role_permissions (role_id, permission, params) VALUES (?, ?, ?)`, id, PermDomainAdminRO, p)
		mustNoErr(t, "store scope "+p, err)
		wantAffected(t, "stored scope rows for "+p, res)
	}
	res, err := db.Exec(`DELETE FROM schema_migrations WHERE version >= 66`)
	mustNoErr(t, "mark the migration as not applied", err)
	unmarked, err := res.RowsAffected()
	mustNoErr(t, "count the unmarked migrations", err)
	wantEq(t, "migration 66 and later were unmarked", unmarked >= 1, true)
	mustNoErr(t, "apply the migration", d.EnsureSchema())

	rows, err := db.Query(`SELECT permission, params FROM role_permissions WHERE role_id = ? ORDER BY permission, params`, id)
	mustNoErr(t, "read the scopes", err)
	defer mustClose(t, rows)
	var got []string
	for rows.Next() {
		var name, params string
		mustNoErr(t, "scan", rows.Scan(&name, &params))
		got = append(got, name+"="+params)
	}
	mustNoErr(t, "iterate the scopes", rows.Err())
	want := []string{PermDomainAdmin + "=*", PermDomainAdminRO + "=5", PermDomainAdminRO + "=7"}
	if !slices.Equal(got, want) {
		t.Errorf("scopes = %v, want %v", got, want)
	}
}

// TestRoleCRUD proves named-role create/get/list/update/delete: name validation
// and uniqueness, the permission set and user assignments round-trip, update
// replaces both sets wholesale, and delete cascades the assignment rows.
func TestRoleCRUD(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	seedDomain(t, d, root, "acme.test")
	alice := mustCreateUser(t, d, root, "alice@acme.test", "password1")
	bob := mustCreateUser(t, d, root, "bob@acme.test", "password1")

	wantRoleNameRules(t, d)

	perms := []Permission{
		{Name: PermSystemAdminRO},
		{Name: PermDomainAdmin, Params: "*"},
		{Name: PermResetPasswd},
	}
	id, err := d.CreateRole("Helpdesk", "Read-only plus password reset", perms, []int64{alice})
	mustNoErr(t, "create role", err)
	wantNonZeroID(t, "role id", id)
	dupID, err := d.CreateRole("Helpdesk", "", nil, nil)
	wantErr(t, "duplicate role name accepted", err)
	wantEq(t, "refused duplicate role id", dupID, int64(0))

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
	unknown, err := d.UpdateRole(999999, "x", "", nil, nil)
	mustNoErr(t, "update an unknown role", err)
	wantEq(t, "UpdateRole on an unknown id", unknown, false)

	// A refused update changes nothing: neither the name, the permissions nor the users.
	updOK1, err := d.UpdateRole(id, "Renamed", "x", []Permission{{Name: "Nonsense"}}, []int64{alice})
	wantErr(t, "an update with an unknown permission accepted", err)
	wantEq(t, "refused unknown perm update reported ok", updOK1, false)
	updOK2, err := d.UpdateRole(id, "", "x", newPerms, []int64{alice})
	wantErr(t, "an update to an empty name accepted", err)
	wantEq(t, "refused empty name update reported ok", updOK2, false)
	got = mustGetRole(t, d, id)
	wantEq(t, "name after the refused updates", got.Name, "Helpdesk RO")
	wantEq(t, "permission count after the refused updates", got.PermCount, 1)
	wantPermissions(t, got.Permissions, newPerms)
	wantOneID(t, "users after the refused updates", got.UserIDs, bob)

	deleted, err := d.DeleteRole(id)
	mustNoErr(t, "delete role", err)
	wantEq(t, "DeleteRole reported the role existed", deleted, true)
	goneRole, stillThere, err := d.GetRole(id)
	mustNoErr(t, "read the deleted role", err)
	wantEq(t, "role present after delete", stillThere, false)
	wantEq(t, "deleted role name empty", goneRole.Name, "")
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
	badID1, err := d.CreateRole("", "x", nil, nil)
	wantErr(t, "empty role name accepted", err)
	wantEq(t, "refused empty role id", badID1, int64(0))
	badID2, err := d.CreateRole(strings.Repeat("a", 65), "x", nil, nil)
	wantErr(t, "65-character role name accepted (the limit is 64)", err)
	wantEq(t, "refused long role id", badID2, int64(0))
	badID3, err := d.CreateRole("Bad", "x", []Permission{{Name: "Nonsense"}}, nil)
	wantErr(t, "role with an unknown permission accepted", err)
	wantEq(t, "refused bad perm role id", badID3, int64(0))
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
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "acme.test")
	mk := func(login string) int64 {
		t.Helper()
		return mustCreateUser(t, d, root, login+"@acme.test", "password1")
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
	// A scoped grant never widens into system authority or another scope.
	if hasPerm(perms, PermSystemAdmin, "") || hasPerm(perms, PermDomainAdmin, "*") || hasPerm(perms, PermOrgAdmin, "*") {
		t.Errorf("scoped grants resolved to wider authority: %+v", perms)
	}
	wantEq(t, "permissions of the scoped user", len(perms), 2)

	// A named role's permissions surface, and overlapping the legacy bridge does
	// not duplicate the permission.
	both := mk("both")
	mustNoErr(t, "grant org admin", d.GrantAdminRole(both, AdminOrg, 9))
	extraID, err := d.CreateRole("Extra", "",
		[]Permission{
			{Name: PermResetPasswd},
			{Name: PermOrgAdmin, Params: "9"}, // duplicates the legacy org grant scope 9
		},
		[]int64{both})
	mustNoErr(t, "create role", err)
	wantEq(t, "extra role id > 0", extraID > 0, true)
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
