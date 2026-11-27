package directory

import (
	"path/filepath"
	"slices"
	"testing"
)

// twoDomainDir seeds two hosted domains with one account each, the smallest
// directory in which one domain can be removed while the other survives.
func twoDomainDir(t *testing.T) (*SQLDirectory, int64) {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()
	var partnerID int64
	var domainIDs []int64
	for _, dom := range []string{"acme.test", "partner.test"} {
		id, err := d.CreateDomain(dom, filepath.Join(root, "domains", dom))
		if err != nil {
			t.Fatal(err)
		}
		domainIDs = append(domainIDs, id)
		if dom == "partner.test" {
			partnerID = id
		}
		uid, err := d.CreateUser("user@"+dom, "secret-pw1", filepath.Join(root, "users", dom))
		if err != nil {
			t.Fatal(err)
		}
		wantEq(t, "created user id > 0 for "+dom, uid > 0, true)
	}
	// One customer runs both domains, so an alias may cross them (an alias
	// stays inside its account's organization).
	org, err := d.CreateOrg("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range domainIDs {
		assigned, err := d.AssignDomainToOrg(id, org)
		if err != nil {
			t.Fatal(err)
		}
		wantEq(t, "assigned domain to org", assigned, true)
	}
	return d, partnerID
}

// TestIdentityDropsWhenItsDomainIsPurged proves an account stops being able to
// claim an address once the server stops hosting its domain. An alias pointing
// into a domain from a user elsewhere is keyed by address string with no foreign
// key, so purging the domain left the row behind, and Identities is exactly what
// the webmail compose gate and the SMTP submission MAIL FROM check accept as a
// From. The account went on originating mail claiming a domain the operator no
// longer had any authority over.
func TestIdentityDropsWhenItsDomainIsPurged(t *testing.T) {
	d, partnerID := twoDomainDir(t)
	const owner, claimed = "user@acme.test", "sales@partner.test"

	// Legitimate today: both domains are hosted.
	if err := d.CreateAlias(claimed, owner); err != nil {
		t.Fatal(err)
	}
	before, err := d.Identities(owner)
	mustNoErr(t, "identities before the purge", err)
	if !slices.Contains(before, claimed) {
		t.Fatalf("the alias is not an identity to begin with: %v", before)
	}

	if ok, err := d.PurgeDomain(partnerID, false); err != nil || !ok {
		t.Fatalf("purge partner.test: %v", err)
	}

	ids, err := d.Identities(owner)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ids, claimed) {
		t.Errorf("the account may still send as an address in a purged domain: %v", ids)
	}
	if !slices.Contains(ids, owner) {
		t.Errorf("the account lost its own address: %v", ids)
	}
	// The row itself must be gone, not merely hidden: anything else reading the
	// table directly would still see it.
	var left int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM aliases WHERE aliasname = ?`, claimed).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d alias row(s) survived the purge", left)
	}
}

// TestIdentityIgnoresAnUnhostedDomain proves the read is guarded too, not only
// the write. A row that predates the checks in CreateAlias, or one written by a
// path that skipped them, must not become a send-as identity just because it is
// in the table.
func TestIdentityIgnoresAnUnhostedDomain(t *testing.T) {
	d, partnerID := twoDomainDir(t)
	wantEq(t, "seeded partner id > 0", partnerID > 0, true)
	const owner = "user@acme.test"

	// Write straight to the tables, the shape a legacy row has.
	res, err := d.db.Exec(`INSERT INTO aliases (aliasname, mainname) VALUES (?, ?)`,
		"ceo@victim.test", owner)
	mustNoErr(t, "write a legacy alias", err)
	wantAffected(t, "legacy alias rows", res)
	res, err = d.db.Exec(`INSERT INTO altnames (user_id, altname) SELECT id, ? FROM users WHERE username = ?`,
		"cfo@victim.test", owner)
	mustNoErr(t, "write a legacy altname", err)
	wantAffected(t, "legacy altname rows", res)
	ids, err := d.Identities(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, unhosted := range []string{"ceo@victim.test", "cfo@victim.test"} {
		if slices.Contains(ids, unhosted) {
			t.Errorf("a legacy row in an unhosted domain is still a send-as identity: %s in %v", unhosted, ids)
		}
	}
}

// TestIdentityStaysOnItsAccount proves an alias belongs only to the account it
// names: another account in the same organization does not gain it as a From.
func TestIdentityStaysOnItsAccount(t *testing.T) {
	d, partnerID := twoDomainDir(t)
	wantEq(t, "seeded partner id > 0", partnerID > 0, true)
	mustNoErr(t, "alias for the partner account", d.CreateAlias("support@acme.test", "user@partner.test"))
	ids, err := d.Identities("user@acme.test")
	mustNoErr(t, "identities of the other account", err)
	if !slices.Equal(ids, []string{"user@acme.test"}) {
		t.Errorf("identities of an account with no aliases = %v, want only its own address", ids)
	}
}

// TestIdentityKeepsHostedAndBareValues is the guard against overreach. The
// ordinary alias must survive, and so must a bare alternative login name, which
// names no domain to claim and exists for the login path.
func TestIdentityKeepsHostedAndBareValues(t *testing.T) {
	d, partnerID := twoDomainDir(t)
	wantEq(t, "seeded partner id > 0", partnerID > 0, true)
	const owner = "user@acme.test"

	if err := d.CreateAlias("sales@acme.test", owner); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.SetAltnames(owner, []string{"legacy-login", "legacy@partner.test"}); err != nil || !ok {
		t.Fatalf("SetAltnames = (%v, %v)", ok, err)
	}
	ids, err := d.Identities(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{owner, "sales@acme.test", "legacy-login", "legacy@partner.test"} {
		if !slices.Contains(ids, want) {
			t.Errorf("%s went missing from %v", want, ids)
		}
	}
}
