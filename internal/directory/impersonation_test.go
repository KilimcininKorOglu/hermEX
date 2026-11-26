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
	d, _ := freshDirectory(t)
	root := t.TempDir()
	acme := mustCreateDomain(t, d, root, "acme.test")
	mustCreateDomain(t, d, root, "other.test")
	mustCreateUser(t, d, root, "boss@acme.test", "password1")
	mustCreateUser(t, d, root, "boss@other.test", "password1")
	scoped := mustCreateUser(t, d, root, "svc@acme.test", "password1")
	everywhere := mustCreateUser(t, d, root, "backup@acme.test", "password1")
	admin := mustCreateUser(t, d, root, "admin@acme.test", "password1")
	mustCreateUser(t, d, root, "plain@acme.test", "password1")

	_, err := d.CreateRole("Migration", "", []Permission{{Name: PermImpersonation, Params: strconv.FormatInt(acme, 10)}}, []int64{scoped})
	mustNoErr(t, "create scoped role", err)
	_, err = d.CreateRole("Backup", "", []Permission{{Name: PermImpersonation, Params: "*"}}, []int64{everywhere})
	mustNoErr(t, "create global role", err)
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
	} {
		got, err := d.MayImpersonate(c.caller, c.target)
		mustNoErr(t, "MayImpersonate", err)
		if got != c.want {
			t.Errorf("MayImpersonate(%s, %s) = %v, want %v", c.caller, c.target, got, c.want)
		}
	}
}
