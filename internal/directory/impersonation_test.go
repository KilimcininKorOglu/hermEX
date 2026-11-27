package directory

import (
	"strconv"
	"testing"
)

// TestMayImpersonateNeedsTheRoleForTheTargetsDomain proves EWS impersonation is
// granted only by the ApplicationImpersonation permission, scoped to the target's
// domain or to every domain, as Exchange's ApplicationImpersonation role is.
// Being an administrator of the domain grants nothing, and neither does an
// unknown target.
func TestMayImpersonateNeedsTheRoleForTheTargetsDomain(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	acme := mustCreateDomain(t, d, root, "acme.test")
	seedDomain(t, d, root, "other.test")
	seedUser(t, d, root, "boss@acme.test", "password1")
	seedUser(t, d, root, "boss@other.test", "password1")
	scoped := mustCreateUser(t, d, root, "svc@acme.test", "password1")
	everywhere := mustCreateUser(t, d, root, "backup@acme.test", "password1")
	admin := mustCreateUser(t, d, root, "admin@acme.test", "password1")
	seedUser(t, d, root, "plain@acme.test", "password1")
	// A scope that names no single domain id is refused, and no role is kept.
	bogus := mustCreateUser(t, d, root, "bogus@acme.test", "password1")
	malformed := " " + strconv.FormatInt(acme, 10) + ",*"
	badID, err := d.CreateRole("Bogus", "", []Permission{{Name: PermImpersonation, Params: malformed}}, []int64{bogus})
	wantErr(t, "a malformed impersonation scope was accepted", err)
	wantEq(t, "refused role id", badID, int64(0))
	wantRows(t, d.db, "roles named Bogus after the refusal", 0, `SELECT COUNT(*) FROM roles WHERE name = 'Bogus'`)
	// A row written before that check existed must still grant nothing: the
	// check compares the whole scope, never a substring of it.
	r3, err := d.CreateRole("Legacy", "", nil, []int64{bogus})
	mustNoErr(t, "create the legacy role", err)
	res, err := d.db.Exec(`INSERT INTO role_permissions (role_id, permission, params) VALUES (?, ?, ?)`, r3, PermImpersonation, malformed)
	mustNoErr(t, "plant the malformed scope", err)
	planted, err := res.RowsAffected()
	mustNoErr(t, "count the planted permission", err)
	wantEq(t, "planted permission rows", planted, int64(1))

	r1, err := d.CreateRole("Migration", "", []Permission{{Name: PermImpersonation, Params: strconv.FormatInt(acme, 10)}}, []int64{scoped})
	mustNoErr(t, "create scoped role", err)
	wantEq(t, "scoped role id > 0", r1 > 0, true)
	r2, err := d.CreateRole("Backup", "", []Permission{{Name: PermImpersonation, Params: "*"}}, []int64{everywhere})
	mustNoErr(t, "create global role", err)
	wantEq(t, "global role id > 0", r2 > 0, true)
	mustNoErr(t, "grant system admin", d.GrantAdminRole(admin, AdminSystem, 0))

	for _, c := range []struct {
		caller, target string
		want           bool
	}{
		{"svc@acme.test", "boss@acme.test", true},
		{"SVC@acme.test", "Boss@Acme.test", true},
		{"svc@acme.test", "boss@other.test", false},
		{"backup@acme.test", "boss@other.test", true},
		{"admin@acme.test", "boss@acme.test", false},
		{"plain@acme.test", "boss@acme.test", false},
		{"svc@acme.test", "nobody@acme.test", false},
		{"nobody@acme.test", "boss@acme.test", false},
		{"", "boss@acme.test", false},
		{"backup@acme.test", "", false},
		{"backup@acme.test", "   ", false},
		{"bogus@acme.test", "boss@acme.test", false},
		{"bogus@acme.test", "boss@other.test", false},
	} {
		got, err := d.MayImpersonate(c.caller, c.target)
		mustNoErr(t, "MayImpersonate", err)
		if got != c.want {
			t.Errorf("MayImpersonate(%s, %s) = %v, want %v", c.caller, c.target, got, c.want)
		}
	}
}
