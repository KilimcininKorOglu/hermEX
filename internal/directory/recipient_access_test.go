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
	if _, err := d.CreateUser("alice@hermex.test", "secret-pw1", aliceDir); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("bob@hermex.test", "secret-pw1", bobDir); err != nil {
		t.Fatal(err)
	}
	return d, aliceDir, bobDir
}

// mustRules lists a user's rules.
func mustRules(t *testing.T, d *SQLDirectory, user string) []RecipientRule {
	t.Helper()
	rules, err := d.ListRecipientRules(user)
	mustNoErr(t, "list "+user+"'s rules", err)
	return rules
}

// mustRuleMap reads the delivery-side rule map of a maildir.
func mustRuleMap(t *testing.T, d *SQLDirectory, maildir string) map[string]string {
	t.Helper()
	m, err := d.RecipientRulesForMaildir(maildir)
	mustNoErr(t, "read the rules of "+maildir, err)
	return m
}

// TestRecipientRuleRoundTrip proves a rule is stored normalised (lowercased) and read
// back through both the management list and the delivery-side maildir map.
func TestRecipientRuleRoundTrip(t *testing.T) {
	d, aliceDir, _ := setupRecipientAccess(t)
	if err := d.SetRecipientRule("alice@hermex.test", "News@Example.com", SenderAllow); err != nil {
		t.Fatal(err)
	}
	rules := mustRules(t, d, "alice@hermex.test")
	if len(rules) != 1 || rules[0].Pattern != "news@example.com" || rules[0].Action != SenderAllow {
		t.Fatalf("list = %+v, want one lowercased allow rule", rules)
	}
	if m := mustRuleMap(t, d, aliceDir); m["news@example.com"] != SenderAllow {
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
	if rules := mustRules(t, d, "alice@hermex.test"); len(rules) != 1 || rules[0].Action != SenderBlock {
		t.Errorf("list = %+v, want a single block rule after the flip", rules)
	}
}

// TestRecipientRuleRefusesAnUnknownAction proves only allow and block are stored, and
// a refused action leaves the existing rule as it was.
func TestRecipientRuleRefusesAnUnknownAction(t *testing.T) {
	d, _, _ := setupRecipientAccess(t)
	mustNoErr(t, "set the rule", d.SetRecipientRule("alice@hermex.test", "x@example.com", SenderAllow))
	for _, bad := range []string{"", "ALLOW", "allowall", "quarantine"} {
		if err := d.SetRecipientRule("alice@hermex.test", "x@example.com", bad); err == nil {
			t.Errorf("the action %q was accepted", bad)
		}
	}
	rules := mustRules(t, d, "alice@hermex.test")
	if len(rules) != 1 || rules[0].Action != SenderAllow {
		t.Errorf("rules after the refusals = %+v, want the one allow rule", rules)
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
		t.Fatalf("delete = (%v, %v), want (true, nil), pattern is matched case-insensitively", ok, err)
	}
	ok, err := d.DeleteRecipientRule("alice@hermex.test", "x@example.com")
	mustNoErr(t, "delete a missing rule", err)
	wantEq(t, "deleting a missing rule reported a deletion", ok, false)
	wantEq(t, "rules in the maildir map after the delete", len(mustRuleMap(t, d, aliceDir)), 0)
}

// TestRecipientRuleDeleteStaysOnItsOwner proves one user deleting a pattern never
// removes another user's rule with the same pattern.
func TestRecipientRuleDeleteStaysOnItsOwner(t *testing.T) {
	d, aliceDir, _ := setupRecipientAccess(t)
	mustNoErr(t, "set alice's rule", d.SetRecipientRule("alice@hermex.test", "x@example.com", SenderBlock))
	ok, err := d.DeleteRecipientRule("bob@hermex.test", "x@example.com")
	mustNoErr(t, "delete as bob", err)
	wantEq(t, "bob's delete found a rule", ok, false)
	wantEq(t, "alice's rule after bob's delete", mustRuleMap(t, d, aliceDir)["x@example.com"], SenderBlock)
}

// TestRecipientRuleUnknownUser proves a rule cannot be set for a user that does not
// exist (so a typo never silently no-ops).
func TestRecipientRuleUnknownUser(t *testing.T) {
	d, _, _ := setupRecipientAccess(t)
	if err := d.SetRecipientRule("ghost@hermex.test", "x@example.com", SenderBlock); err == nil {
		t.Error("setting a rule for an unknown user must error")
	}
}

// TestRecipientRuleRefusesAPatternNoSenderMatches sets a wildcard personal rule.
// Rules are matched by exact lookup, so it would be listed as active and block
// nothing; it is refused and no row is kept.
func TestRecipientRuleRefusesAPatternNoSenderMatches(t *testing.T) {
	d, _, _ := setupRecipientAccess(t)
	if err := d.SetRecipientRule("alice@hermex.test", "*.example.com", SenderBlock); err == nil {
		t.Error("a wildcard personal rule was accepted")
	}
	if rules := mustRules(t, d, "alice@hermex.test"); len(rules) != 0 {
		t.Errorf("personal rules after the refusal = %+v, want none", rules)
	}
}

// TestRecipientRulesAreIsolated proves rules are per-recipient: one mailbox's rules
// never leak into another's delivery-side map.
func TestRecipientRulesAreIsolated(t *testing.T) {
	d, aliceDir, bobDir := setupRecipientAccess(t)
	if err := d.SetRecipientRule("alice@hermex.test", "friend@partner.example", SenderAllow); err != nil {
		t.Fatal(err)
	}
	if m := mustRuleMap(t, d, bobDir); len(m) != 0 {
		t.Errorf("bob sees %v, but alice's rule must not apply to bob", m)
	}
	if m := mustRuleMap(t, d, aliceDir); m["friend@partner.example"] != SenderAllow {
		t.Errorf("alice's own rule missing from her map: %v", m)
	}
}

// TestRecipientRulesMatchServerWideParity is the parity check (the invariant that a
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
	list := antispam.NewAccessList(mustRuleMap(t, d, aliceDir))
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
	wantRows(t, d.db, "recipient_access rows after the owner was deleted", 0, `SELECT COUNT(*) FROM recipient_access`)
	wantEq(t, "rules in the maildir map after the owner was deleted", len(mustRuleMap(t, d, aliceDir)), 0)
}
