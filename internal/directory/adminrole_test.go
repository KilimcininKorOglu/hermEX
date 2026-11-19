package directory

import "testing"

// TestAdminRoles proves a login resolves to its user id, admin roles round-trip
// (grant is idempotent), and an unknown role tier is rejected.
func TestAdminRoles(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	uid := mustCreateUser(t, d, root, "admin@hermex.test", "pw")

	id, ok, err := d.UserID("admin@hermex.test")
	mustNoErr(t, "resolve the login", err)
	wantEq(t, "UserID found the login", ok, true)
	wantEq(t, "resolved user id", id, uid)
	_, found, _ := d.UserID("ghost@hermex.test")
	wantEq(t, "UserID resolved an unknown login", found, false)

	wantEq(t, "roles of a fresh user", len(mustAdminRoles(t, d, uid)), 0)

	mustNoErr(t, "grant system admin", d.GrantAdminRole(uid, AdminSystem, 0))
	mustNoErr(t, "grant org admin", d.GrantAdminRole(uid, AdminOrg, 5))
	mustNoErr(t, "re-grant org admin (idempotent)", d.GrantAdminRole(uid, AdminOrg, 5))

	roles := mustAdminRoles(t, d, uid)
	if len(roles) != 2 {
		t.Fatalf("AdminRoles = %v, want 2 (system + org:5)", roles)
	}
	wantEq(t, "system grant present", hasRole(roles, AdminSystem, 0), true)
	wantEq(t, "org:5 grant present", hasRole(roles, AdminOrg, 5), true)

	wantErr(t, "GrantAdminRole accepted an unknown role tier", d.GrantAdminRole(uid, "wizard", 0))
	// A scope that does not fit the role grants nothing, so it is refused: an org or
	// domain role with no scope (0 or negative), and a system role with one.
	wantErr(t, "GrantAdminRole accepted an org role with scope 0", d.GrantAdminRole(uid, AdminOrg, 0))
	wantErr(t, "GrantAdminRole accepted a domain role with scope -1", d.GrantAdminRole(uid, AdminDomain, -1))
	wantErr(t, "GrantAdminRole accepted a system role with a scope", d.GrantAdminRole(uid, AdminSystem, 3))
}

// mustAdminRoles reads a user's direct admin grants.
func mustAdminRoles(t *testing.T, d *SQLDirectory, uid int64) []AdminRole {
	t.Helper()
	roles, err := d.AdminRoles(uid)
	mustNoErr(t, "read admin roles", err)
	return roles
}

// hasRole reports whether a grant list carries an exact (tier, scope) pair.
func hasRole(roles []AdminRole, role string, scope int64) bool {
	for _, r := range roles {
		if r.Role == role && r.ScopeID == scope {
			return true
		}
	}
	return false
}
