package directory

import (
	"path/filepath"
	"testing"

	"hermex/internal/antispam"
)

// setupRecipientAccess builds a directory with two mailboxes (alice, bob) in one
// domain and returns their maildirs, so tests can prove rules are per-recipient.
func setupRecipientAccess(t *testing.T) (d *SQLDirectory, aliceDir, bobDir string) {
	t.Helper()
	db := openTestDB(t)
	d = NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "dom")); err != nil {
		t.Fatal(err)
	}
	aliceDir = filepath.Join(root, "users", "alice")
	bobDir = filepath.Join(root, "users", "bob")
	if _, err := d.CreateUser("alice@hermex.test", "secret", aliceDir); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("bob@hermex.test", "secret", bobDir); err != nil {
		t.Fatal(err)
	}
	return d, aliceDir, bobDir
}

// TestRecipientRuleRoundTrip proves a rule is stored normalised (lowercased) and read
// back through both the management list and the delivery-side maildir map.
func TestRecipientRuleRoundTrip(t *testing.T) {
	d, aliceDir, _ := setupRecipientAccess(t)
	if err := d.SetRecipientRule("alice@hermex.test", "News@Example.com", SenderAllow); err != nil {
		t.Fatal(err)
	}
	rules, err := d.ListRecipientRules("alice@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Pattern != "news@example.com" || rules[0].Action != SenderAllow {
		t.Fatalf("list = %+v, want one lowercased allow rule", rules)
	}
	m, err := d.RecipientRulesForMaildir(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	if m["news@example.com"] != SenderAllow {
		t.Errorf("maildir map = %v, want news@example.com -> allow", m)
	}
}

// TestRecipientRuleUpsertFlips proves re-adding a pattern with the other action flips
// it rather than creating a duplicate, so a pattern always carries exactly one action.
func TestRecipientRuleUpsertFlips(t *testing.T) {
	d, _, _ := setupRecipientAccess(t)
	if err := d.SetRecipientRule("alice@hermex.test", "x@example.com", SenderAllow); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRecipientRule("alice@hermex.test", "x@example.com", SenderBlock); err != nil {
		t.Fatal(err)
	}
	rules, _ := d.ListRecipientRules("alice@hermex.test")
	if len(rules) != 1 || rules[0].Action != SenderBlock {
		t.Errorf("list = %+v, want a single block rule after the flip", rules)
	}
}

// TestRecipientRuleDelete proves a rule is removable and that deleting a missing rule
// reports ok=false.
func TestRecipientRuleDelete(t *testing.T) {
	d, aliceDir, _ := setupRecipientAccess(t)
	if err := d.SetRecipientRule("alice@hermex.test", "x@example.com", SenderBlock); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.DeleteRecipientRule("alice@hermex.test", "X@Example.com"); err != nil || !ok {
		t.Fatalf("delete = (%v, %v), want (true, nil) — pattern is matched case-insensitively", ok, err)
	}
	if ok, _ := d.DeleteRecipientRule("alice@hermex.test", "x@example.com"); ok {
		t.Error("deleting a missing rule must report ok=false")
	}
	if m, _ := d.RecipientRulesForMaildir(aliceDir); len(m) != 0 {
		t.Errorf("maildir map = %v, want empty after delete", m)
	}
}

// TestRecipientRuleUnknownUser proves a rule cannot be set for a user that does not
// exist (so a typo never silently no-ops).
func TestRecipientRuleUnknownUser(t *testing.T) {
	d, _, _ := setupRecipientAccess(t)
	if err := d.SetRecipientRule("ghost@hermex.test", "x@example.com", SenderBlock); err == nil {
		t.Error("setting a rule for an unknown user must error")
	}
}

// TestRecipientRulesAreIsolated proves rules are per-recipient: one mailbox's rules
// never leak into another's delivery-side map.
func TestRecipientRulesAreIsolated(t *testing.T) {
	d, aliceDir, bobDir := setupRecipientAccess(t)
	if err := d.SetRecipientRule("alice@hermex.test", "friend@partner.example", SenderAllow); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.RecipientRulesForMaildir(bobDir); len(m) != 0 {
		t.Errorf("bob sees %v, but alice's rule must not apply to bob", m)
	}
	if m, _ := d.RecipientRulesForMaildir(aliceDir); m["friend@partner.example"] != SenderAllow {
		t.Errorf("alice's own rule missing from her map: %v", m)
	}
}

// TestRecipientRulesMatchServerWideParity is the parity teeth (the invariant that a
// per-recipient rule must match a sender identically to a server-wide rule): the
// stored rules, fed to the same matcher the operator list uses, must resolve the
// exact-address > envelope-domain > From-header-domain precedence and ignore a bounce.
func TestRecipientRulesMatchServerWideParity(t *testing.T) {
	d, aliceDir, _ := setupRecipientAccess(t)
	for pattern, action := range map[string]string{
		"vip@example.com": SenderAllow,
		"example.com":     SenderBlock,
		"blocked.example": SenderBlock,
	} {
		if err := d.SetRecipientRule("alice@hermex.test", pattern, action); err != nil {
			t.Fatal(err)
		}
	}
	m, err := d.RecipientRulesForMaildir(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	list := antispam.NewAccessList(m)
	cases := []struct{ mailFrom, fromDomain, want string }{
		{"vip@example.com", "", antispam.AccessAllow},                // exact beats the domain block
		{"other@example.com", "", antispam.AccessBlock},              // domain rule applies to non-exact
		{"a@clean.example", "blocked.example", antispam.AccessBlock}, // From-header domain, domain tier
		{"a@clean.example", "", ""},                                  // no rule
		{"", "example.com", ""},                                      // a bounce matches nothing
		{"a@sub.example.com", "", ""},                                // exact-domain only: subdomain not covered
	}
	for _, c := range cases {
		if got := list.Action(c.mailFrom, c.fromDomain); got != c.want {
			t.Errorf("Action(%q, %q) = %q, want %q", c.mailFrom, c.fromDomain, got, c.want)
		}
	}
}

// TestRecipientRulesCascadeOnUserDelete proves deleting the owning user removes its
// rules through the FK cascade, so a purged mailbox leaves no dangling rows that an
// AUTO_INCREMENT-reused user id could later re-attach.
func TestRecipientRulesCascadeOnUserDelete(t *testing.T) {
	d, aliceDir, _ := setupRecipientAccess(t)
	if err := d.SetRecipientRule("alice@hermex.test", "x@example.com", SenderBlock); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.DeleteUser("alice@hermex.test", false); err != nil || !ok {
		t.Fatalf("delete user = (%v, %v), want (true, nil)", ok, err)
	}
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM recipient_access`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("recipient_access has %d rows after the owner was deleted, want 0 (cascade failed)", n)
	}
	if m, _ := d.RecipientRulesForMaildir(aliceDir); len(m) != 0 {
		t.Errorf("maildir map = %v, want empty after the owner was deleted", m)
	}
}
