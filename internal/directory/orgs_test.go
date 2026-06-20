package directory

import (
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/easpolicy"
)

// TestOrgCRUD proves organization create/get/list/update, name validation and
// uniqueness, and the per-domain attach/detach association with its domain count.
func TestOrgCRUD(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	if _, err := d.CreateOrg("", "x"); err == nil {
		t.Error("empty org name accepted")
	}
	if _, err := d.CreateOrg(strings.Repeat("a", 33), "x"); err == nil {
		t.Error("33-character org name accepted (limit is 32)")
	}

	id, err := d.CreateOrg("Acme", "The Acme organization")
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("org id 0 issued — it collides with the reserved organizationless sentinel")
	}
	if _, err := d.CreateOrg("Acme", ""); err == nil {
		t.Error("duplicate org name accepted")
	}

	got, ok, err := d.GetOrg(id)
	if err != nil || !ok || got.Name != "Acme" || got.Description != "The Acme organization" || got.DomainCount != 0 {
		t.Fatalf("GetOrg = %+v, %v, %v", got, ok, err)
	}

	root := t.TempDir()
	domID, err := d.CreateDomain("acme.test", filepath.Join(root, "acme.test"))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.AssignDomainToOrg(domID, id); err != nil || !ok {
		t.Fatalf("AssignDomainToOrg = %v, %v", ok, err)
	}
	if got, _, _ := d.GetOrg(id); got.DomainCount != 1 {
		t.Errorf("domain count after attach = %d, want 1", got.DomainCount)
	}
	if _, err := d.AssignDomainToOrg(domID, 999999); err == nil {
		t.Error("assigning a domain to a nonexistent org was accepted")
	}
	if ok, err := d.AssignDomainToOrg(888888, id); err != nil || ok {
		t.Errorf("assign unknown domain = %v, %v; want false, nil", ok, err)
	}
	if ok, err := d.AssignDomainToOrg(domID, 0); err != nil || !ok {
		t.Fatalf("detach = %v, %v", ok, err)
	}
	if got, _, _ := d.GetOrg(id); got.DomainCount != 0 {
		t.Errorf("domain count after detach = %d, want 0", got.DomainCount)
	}

	if ok, err := d.UpdateOrg(id, "Acme Inc", "new desc"); err != nil || !ok {
		t.Fatalf("UpdateOrg = %v, %v", ok, err)
	}
	if got, _, _ := d.GetOrg(id); got.Name != "Acme Inc" || got.Description != "new desc" {
		t.Errorf("after update = %+v", got)
	}
	if ok, _ := d.UpdateOrg(999999, "x", ""); ok {
		t.Error("UpdateOrg on an unknown id returned ok=true")
	}

	orgs, err := d.ListOrgs()
	if err != nil || len(orgs) != 1 || orgs[0].Name != "Acme Inc" {
		t.Fatalf("ListOrgs = %+v, %v", orgs, err)
	}
}

// TestDeleteOrgCascade proves DeleteOrg detaches the org's domains (org_id 0,
// not deleted), removes its org-scoped configuration (LDAP, sync policy,
// org-admin grants), refuses the reserved id 0, and — the landmine — never
// touches the global default sync policy stored on org_id 0.
func TestDeleteOrgCascade(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	// Global default device policy on the reserved org 0 — must survive any org delete.
	if err := d.SetDefaultSyncPolicy(easpolicy.Policy{"DevicePasswordEnabled": 1}); err != nil {
		t.Fatal(err)
	}

	id, err := d.CreateOrg("Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	domID, err := d.CreateDomain("acme.test", filepath.Join(root, "acme.test"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AssignDomainToOrg(domID, id); err != nil {
		t.Fatal(err)
	}
	if err := d.SetLDAPConfig(id, LDAPConfig{URI: "ldap://acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sync_policy (org_id, policy) VALUES (?, '{}')`, id); err != nil {
		t.Fatal(err)
	}
	uid, err := d.CreateUser("admin@acme.test", "pw", filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.GrantAdminRole(uid, AdminOrg, id); err != nil {
		t.Fatal(err)
	}

	if ok, err := d.DeleteOrg(0); ok || err == nil {
		t.Errorf("DeleteOrg(0) = %v, %v; want false + error (reserved sentinel)", ok, err)
	}

	if ok, err := d.DeleteOrg(id); err != nil || !ok {
		t.Fatalf("DeleteOrg = %v, %v", ok, err)
	}

	if _, ok, _ := d.GetOrg(id); ok {
		t.Error("org still present after delete")
	}
	var domOrg int64
	if err := db.QueryRow(`SELECT org_id FROM domains WHERE id = ?`, domID).Scan(&domOrg); err != nil {
		t.Fatalf("domain was deleted with its org: %v", err)
	}
	if domOrg != 0 {
		t.Errorf("domain org_id = %d after org delete, want 0 (detached)", domOrg)
	}
	if _, ok, _ := d.GetLDAPConfig(id); ok {
		t.Error("org ldap_config survived the org delete")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_policy WHERE org_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("org sync_policy rows = %d after delete, want 0", n)
	}
	roles, _ := d.AdminRoles(uid)
	for _, r := range roles {
		if r.Role == AdminOrg && r.ScopeID == id {
			t.Error("org-admin grant survived the org delete")
		}
	}
	// The landmine: the global default sync policy (org_id 0) must be untouched.
	if got, _ := d.GetDefaultSyncPolicy(); got == nil || got["DevicePasswordEnabled"] != 1 {
		t.Errorf("global default sync policy (org 0) wiped by an org delete: %v", got)
	}

	if ok, err := d.DeleteOrg(999999); ok || err != nil {
		t.Errorf("DeleteOrg(unknown) = %v, %v; want false, nil", ok, err)
	}
}
