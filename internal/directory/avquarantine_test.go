package directory

import (
	"path/filepath"
	"testing"
)

// TestQuarantineCRUD proves a quarantined message round-trips (recipients
// reassembled), an unknown id is a clean miss, and the domain-scoped list only
// returns records a given admin scope may see.
func TestQuarantineCRUD(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	dom, err := d.CreateDomain("acme.test", filepath.Join(root, "acme"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.CreateDomain("other.test", filepath.Join(root, "other"))
	if err != nil {
		t.Fatal(err)
	}

	id, err := d.QuarantineMessage(QuarantineEntry{
		Direction:    "inbound",
		MailFrom:     "evil@spam.example",
		Recipients:   []string{"victim@acme.test", "cc@acme.test"},
		Subject:      "invoice",
		VirusName:    "Eicar-Test-Signature",
		InfectedFile: "invoice.exe",
		DomainID:     dom,
		CreatedAt:    1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("QuarantineMessage returned id 0")
	}

	rec, ok, err := d.GetQuarantine(id)
	if err != nil || !ok {
		t.Fatalf("GetQuarantine = (%v, %v)", ok, err)
	}
	if rec.VirusName != "Eicar-Test-Signature" || rec.MailFrom != "evil@spam.example" ||
		rec.InfectedFile != "invoice.exe" || rec.DomainID != dom || rec.Status != "held" {
		t.Fatalf("record mismatch: %+v", rec)
	}
	if len(rec.Recipients) != 2 || rec.Recipients[0] != "victim@acme.test" {
		t.Fatalf("recipients = %v, want 2 reassembled", rec.Recipients)
	}

	if _, ok, err := d.GetQuarantine(id + 999); ok || err != nil {
		t.Fatalf("GetQuarantine(unknown) = (%v, %v), want (false, nil)", ok, err)
	}

	// Scoping: system (all) sees it, the owning domain sees it, another domain
	// and an empty scope see nothing.
	if recs, err := d.ListQuarantine(nil, true, 0); err != nil || len(recs) != 1 {
		t.Fatalf("ListQuarantine(all) = (%d, %v), want 1", len(recs), err)
	}
	if recs, err := d.ListQuarantine([]int64{dom}, false, 0); err != nil || len(recs) != 1 {
		t.Fatalf("ListQuarantine([dom]) = (%d, %v), want 1", len(recs), err)
	}
	if recs, err := d.ListQuarantine([]int64{other}, false, 0); err != nil || len(recs) != 0 {
		t.Fatalf("ListQuarantine([other]) = (%d, %v), want 0", len(recs), err)
	}
	if recs, err := d.ListQuarantine(nil, false, 0); err != nil || len(recs) != 0 {
		t.Fatalf("ListQuarantine(no scope) = (%d, %v), want 0", len(recs), err)
	}

	if err := d.DeleteQuarantine(id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.GetQuarantine(id); ok {
		t.Error("record survived DeleteQuarantine")
	}
}

// TestDomainOrgAdminEmails proves the notification resolver returns a domain's
// domain admins plus its organization's org admins, and excludes non-admins.
func TestDomainOrgAdminEmails(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	dom, err := d.CreateDomain("acme.test", filepath.Join(root, "acme"))
	if err != nil {
		t.Fatal(err)
	}
	org, err := d.CreateOrg("acme-org", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AssignDomainToOrg(dom, org); err != nil {
		t.Fatal(err)
	}

	mk := func(addr string) int64 {
		uid, err := d.CreateUser(addr, "pw", filepath.Join(root, addr))
		if err != nil {
			t.Fatal(err)
		}
		return uid
	}
	dadmin := mk("dadmin@acme.test")
	oadmin := mk("oadmin@acme.test")
	mk("plain@acme.test") // no admin role

	if err := d.GrantAdminRole(dadmin, AdminDomain, dom); err != nil {
		t.Fatal(err)
	}
	if err := d.GrantAdminRole(oadmin, AdminOrg, org); err != nil {
		t.Fatal(err)
	}

	emails, err := d.DomainOrgAdminEmails(dom)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range emails {
		got[e] = true
	}
	if !got["dadmin@acme.test"] || !got["oadmin@acme.test"] {
		t.Fatalf("emails = %v, want both the domain and org admin", emails)
	}
	if got["plain@acme.test"] {
		t.Error("DomainOrgAdminEmails included a non-admin")
	}
}
