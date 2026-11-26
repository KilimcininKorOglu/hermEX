package directory

import "testing"

// TestQuarantineCRUD proves a quarantined message round-trips (recipients
// reassembled), an unknown id is a clean miss, and the domain-scoped list only
// returns records a given admin scope may see.
func TestQuarantineCRUD(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	dom := mustCreateDomain(t, d, root, "acme.test")
	other := mustCreateDomain(t, d, root, "other.test")

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
	mustNoErr(t, "quarantine message", err)
	wantNonZeroID(t, "quarantine id", id)

	rec, ok, err := d.GetQuarantine(id)
	mustNoErr(t, "get quarantine record", err)
	wantEq(t, "record found", ok, true)
	wantEq(t, "virus name", rec.VirusName, "Eicar-Test-Signature")
	wantEq(t, "mail from", rec.MailFrom, "evil@spam.example")
	wantEq(t, "infected file", rec.InfectedFile, "invoice.exe")
	wantEq(t, "domain id", rec.DomainID, dom)
	wantEq(t, "status", rec.Status, "held")
	if len(rec.Recipients) != 2 {
		t.Fatalf("recipients = %v, want 2 reassembled", rec.Recipients)
	}
	wantEq(t, "first recipient", rec.Recipients[0], "victim@acme.test")

	_, found, err := d.GetQuarantine(id + 999)
	mustNoErr(t, "get an unknown record", err)
	wantEq(t, "GetQuarantine(unknown) found", found, false)

	// Scoping: system (all) sees it, the owning domain sees it, another domain
	// and an empty scope see nothing.
	wantEq(t, "records a system admin sees", len(mustListQuarantine(t, d, nil, true)), 1)
	wantEq(t, "records the owning domain sees", len(mustListQuarantine(t, d, []int64{dom}, false)), 1)
	wantEq(t, "records another domain sees", len(mustListQuarantine(t, d, []int64{other}, false)), 0)
	wantEq(t, "records an empty scope sees", len(mustListQuarantine(t, d, nil, false)), 0)

	mustNoErr(t, "delete quarantine record", d.DeleteQuarantine(id))
	_, stillThere, err := d.GetQuarantine(id)
	mustNoErr(t, "get the deleted record", err)
	wantEq(t, "record present after delete", stillThere, false)
}

// mustListQuarantine lists the quarantine records one admin scope may see.
func mustListQuarantine(t *testing.T, d *SQLDirectory, domains []int64, all bool) []QuarantineRecord {
	t.Helper()
	recs, err := d.ListQuarantine(domains, all, 0)
	mustNoErr(t, "list quarantine records", err)
	return recs
}

// TestDomainOrgAdminEmails proves the notification resolver returns a domain's
// domain admins plus its organization's org admins, and excludes non-admins.
func TestDomainOrgAdminEmails(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	dom := mustCreateDomain(t, d, root, "acme.test")
	org, err := d.CreateOrg("acme-org", "")
	mustNoErr(t, "create org", err)
	_, err = d.AssignDomainToOrg(dom, org)
	mustNoErr(t, "assign the domain to the org", err)

	dadmin := mustCreateUser(t, d, root, "dadmin@acme.test", "password1")
	oadmin := mustCreateUser(t, d, root, "oadmin@acme.test", "password1")
	mustCreateUser(t, d, root, "plain@acme.test", "password1") // no admin role

	mustNoErr(t, "grant domain admin", d.GrantAdminRole(dadmin, AdminDomain, dom))
	mustNoErr(t, "grant org admin", d.GrantAdminRole(oadmin, AdminOrg, org))

	// Admins of another tenant: a domain admin of another domain and an org admin
	// of another organization must not learn of acme.test's quarantine.
	otherDom := mustCreateDomain(t, d, root, "other.test")
	otherOrg, err := d.CreateOrg("other-org", "")
	mustNoErr(t, "create the other org", err)
	_, err = d.AssignDomainToOrg(otherDom, otherOrg)
	mustNoErr(t, "assign the other domain to the other org", err)
	odadmin := mustCreateUser(t, d, root, "dadmin@other.test", "password1")
	ooadmin := mustCreateUser(t, d, root, "oadmin@other.test", "password1")
	mustNoErr(t, "grant the other domain admin", d.GrantAdminRole(odadmin, AdminDomain, otherDom))
	mustNoErr(t, "grant the other org admin", d.GrantAdminRole(ooadmin, AdminOrg, otherOrg))

	emails, err := d.DomainOrgAdminEmails(dom)
	mustNoErr(t, "resolve the notification addresses", err)
	got := map[string]bool{}
	for _, e := range emails {
		got[e] = true
	}
	for addr, want := range map[string]bool{
		"dadmin@acme.test":  true,
		"oadmin@acme.test":  true,
		"plain@acme.test":   false,
		"dadmin@other.test": false,
		"oadmin@other.test": false,
	} {
		wantEq(t, addr+" is notified", got[addr], want)
	}
}
