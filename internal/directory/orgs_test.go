package directory

import (
	"database/sql"
	"slices"
	"strings"
	"testing"

	"hermex/internal/easpolicy"
)

// TestOrgCRUD proves organization create/get/list/update, name validation and
// uniqueness, and the per-domain attach/detach association with its domain count.
func TestOrgCRUD(t *testing.T) {
	d, _ := freshDirectory(t)

	_, err := d.CreateOrg("", "x")
	wantErr(t, "empty org name accepted", err)
	_, err = d.CreateOrg(strings.Repeat("a", 33), "x")
	wantErr(t, "33-character org name accepted (the limit is 32)", err)

	id, err := d.CreateOrg("Acme", "The Acme organization")
	mustNoErr(t, "create org", err)
	if id == 0 {
		t.Fatal("org id 0 issued, it collides with the reserved organizationless sentinel")
	}
	_, err = d.CreateOrg("Acme", "")
	wantErr(t, "duplicate org name accepted", err)

	got := mustGetOrg(t, d, id)
	wantEq(t, "org name", got.Name, "Acme")
	wantEq(t, "org description", got.Description, "The Acme organization")
	wantEq(t, "domain count of a fresh org", got.DomainCount, 0)

	root := t.TempDir()
	domID := mustCreateDomain(t, d, root, "acme.test")
	wantDomainAttachDetach(t, d, id, domID)

	ok, err := d.UpdateOrg(id, "Acme Inc", "new desc")
	mustNoErr(t, "update org", err)
	wantEq(t, "UpdateOrg found the org", ok, true)
	got = mustGetOrg(t, d, id)
	wantEq(t, "name after update", got.Name, "Acme Inc")
	wantEq(t, "description after update", got.Description, "new desc")
	unknown, err := d.UpdateOrg(999999, "x", "")
	mustNoErr(t, "update an unknown org", err)
	wantEq(t, "UpdateOrg on an unknown id", unknown, false)

	// A refused rename keeps the stored name: an empty name, one over the limit,
	// and another organization's name.
	_, err = d.CreateOrg("Other", "")
	mustNoErr(t, "create a second org", err)
	for _, bad := range []string{"", strings.Repeat("a", 33), "Other"} {
		_, err := d.UpdateOrg(id, bad, "changed")
		wantErr(t, "UpdateOrg accepted the name "+bad, err)
		got = mustGetOrg(t, d, id)
		wantEq(t, "name after a refused rename", got.Name, "Acme Inc")
		wantEq(t, "description after a refused rename", got.Description, "new desc")
	}

	orgs, err := d.ListOrgs()
	mustNoErr(t, "list orgs", err)
	if len(orgs) != 2 {
		t.Fatalf("ListOrgs = %+v, want two orgs", orgs)
	}
	names := []string{orgs[0].Name, orgs[1].Name}
	slices.Sort(names)
	wantEq(t, "listed org names", strings.Join(names, ","), "Acme Inc,Other")
}

// wantDomainAttachDetach proves a domain attaches to an org and detaches back to
// the organizationless sentinel, with the org's domain count following, and that
// neither an unknown org nor an unknown domain is accepted.
func wantDomainAttachDetach(t *testing.T, d *SQLDirectory, orgID, domID int64) {
	t.Helper()
	ok, err := d.AssignDomainToOrg(domID, orgID)
	mustNoErr(t, "assign domain to org", err)
	wantEq(t, "AssignDomainToOrg found both rows", ok, true)
	wantEq(t, "domain count after attach", mustGetOrg(t, d, orgID).DomainCount, 1)

	_, err = d.AssignDomainToOrg(domID, 999999)
	wantErr(t, "assigning a domain to a nonexistent org accepted", err)
	wantEq(t, "domain count after the refused move", mustGetOrg(t, d, orgID).DomainCount, 1)
	unknownDomain, err := d.AssignDomainToOrg(888888, orgID)
	mustNoErr(t, "assign an unknown domain", err)
	wantEq(t, "assigning an unknown domain", unknownDomain, false)

	ok, err = d.AssignDomainToOrg(domID, 0)
	mustNoErr(t, "detach domain", err)
	wantEq(t, "detach found the domain", ok, true)
	wantEq(t, "domain count after detach", mustGetOrg(t, d, orgID).DomainCount, 0)
}

// mustGetOrg reads an org back, requiring it to exist.
func mustGetOrg(t *testing.T, d *SQLDirectory, id int64) OrgInfo {
	t.Helper()
	got, ok, err := d.GetOrg(id)
	mustNoErr(t, "get org", err)
	if !ok {
		t.Fatalf("org %d not found", id)
	}
	return got
}

// TestDeleteOrgCascade proves DeleteOrg detaches the org's domains (org_id 0,
// not deleted, keeping their directory binding), removes its org-scoped
// configuration (sync policy, org-admin grants), refuses the reserved id 0, and,
// the landmine, never touches the global default sync policy stored on org_id 0.
func TestDeleteOrgCascade(t *testing.T) {
	d, db := freshDirectory(t)

	// Global default device policy on the reserved org 0, must survive any org delete.
	mustNoErr(t, "set the default sync policy",
		d.SetDefaultSyncPolicy(easpolicy.Policy{"DevicePasswordEnabled": 1}))

	id, err := d.CreateOrg("Acme", "")
	mustNoErr(t, "create org", err)
	root := t.TempDir()
	domID, uid := seedOrgScopedConfig(t, d, db, root, id)

	refused, err := d.DeleteOrg(0)
	wantEq(t, "DeleteOrg(0) removed the reserved sentinel", refused, false)
	wantErr(t, "DeleteOrg(0) accepted the reserved sentinel", err)
	wantEq(t, "the org's domain count after the refused delete", mustGetOrg(t, d, id).DomainCount, 1)

	deleted, err := d.DeleteOrg(id)
	mustNoErr(t, "delete org", err)
	wantEq(t, "DeleteOrg reported the org existed", deleted, true)

	_, stillThere, err := d.GetOrg(id)
	mustNoErr(t, "read the deleted org", err)
	wantEq(t, "org present after delete", stillThere, false)
	var domOrg int64
	mustNoErr(t, "the domain was deleted with its org",
		db.QueryRow(`SELECT org_id FROM domains WHERE id = ?`, domID).Scan(&domOrg))
	wantEq(t, "domain org_id after the org delete (detached)", domOrg, int64(0))
	_, bound, err := d.LDAPBindingForDomain(domID)
	mustNoErr(t, "read the domain's binding", err)
	wantEq(t, "the detached domain keeps its directory binding", bound, true)
	wantRows(t, db, "org sync_policy rows after delete", 0, `SELECT COUNT(*) FROM sync_policy WHERE org_id = ?`, id)
	roles, err := d.AdminRoles(uid)
	mustNoErr(t, "read admin roles", err)
	for _, r := range roles {
		if r.Role == AdminOrg && r.ScopeID == id {
			t.Error("org-admin grant survived the org delete")
		}
	}
	// The landmine: the global default sync policy (org_id 0) must be untouched.
	got, err := d.GetDefaultSyncPolicy()
	mustNoErr(t, "read the default sync policy", err)
	if got == nil || got["DevicePasswordEnabled"] != 1 {
		t.Errorf("global default sync policy (org 0) wiped by an org delete: %v", got)
	}

	missing, err := d.DeleteOrg(999999)
	mustNoErr(t, "delete an unknown org", err)
	wantEq(t, "DeleteOrg(unknown)", missing, false)
}

// seedOrgScopedConfig attaches a domain to the org, binds the domain to a
// directory connection, and gives the org one row of each org-scoped kind the
// delete must clear: a sync policy and an org-admin grant. It returns the domain
// and the granted user.
func seedOrgScopedConfig(t *testing.T, d *SQLDirectory, db *sql.DB, root string, orgID int64) (domID, uid int64) {
	t.Helper()
	domID = mustCreateDomain(t, d, root, "acme.test")
	_, err := d.AssignDomainToOrg(domID, orgID)
	mustNoErr(t, "assign domain to org", err)
	mustBindDomain(t, d, domID, LDAPConnection{Name: "acme", URI: "ldaps://acme"})
	_, err = db.Exec(`INSERT INTO sync_policy (org_id, policy) VALUES (?, '{}')`, orgID)
	mustNoErr(t, "insert the org sync policy", err)
	uid = mustCreateUser(t, d, root, "admin@acme.test", "password1")
	mustNoErr(t, "grant org admin", d.GrantAdminRole(uid, AdminOrg, orgID))
	return domID, uid
}
