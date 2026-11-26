package directory

import "testing"

func setupSenderAccess(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM sender_access"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestSenderRuleRefusesAPatternNoSenderMatches sets a wildcard operator rule.
// Rules are matched by exact lookup, so it would be listed as active and block
// nothing; it is refused and no row is kept.
func TestSenderRuleRefusesAPatternNoSenderMatches(t *testing.T) {
	d := setupSenderAccess(t)
	if err := d.SetSenderRule("*", SenderBlock); err == nil {
		t.Error("a wildcard operator rule was accepted")
	}
	if rules, err := d.ListSenderRules(); err != nil || len(rules) != 0 {
		t.Errorf("operator rules after the refusal = %+v (err %v), want none", rules, err)
	}
}

// TestSenderRuleRoundTrip proves rules are stored lowercased and listed back, and
// that the pattern is normalised on the way in.
func TestSenderRuleRoundTrip(t *testing.T) {
	d := setupSenderAccess(t)
	if err := d.SetSenderRule("  Partner@Example.COM ", SenderAllow); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSenderRule("spammy.example", SenderBlock); err != nil {
		t.Fatal(err)
	}
	rules, err := d.ListSenderRules()
	if err != nil || len(rules) != 2 {
		t.Fatalf("ListSenderRules = %+v (err %v), want 2 rules", rules, err)
	}
	// The list is ordered by action, then pattern; "allow" sorts before "block".
	if rules[0].Pattern != "partner@example.com" || rules[0].Action != SenderAllow {
		t.Errorf("rule[0] = %+v, want the normalised allow rule", rules[0])
	}
	if rules[1].Pattern != "spammy.example" || rules[1].Action != SenderBlock {
		t.Errorf("rule[1] = %+v, want the block rule", rules[1])
	}
}

// TestSenderRuleFlip proves re-adding a pattern with the other action flips it in
// place (one row, not two), so a pattern always carries exactly one action.
func TestSenderRuleFlip(t *testing.T) {
	d := setupSenderAccess(t)
	if err := d.SetSenderRule("x@example.com", SenderAllow); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSenderRule("x@example.com", SenderBlock); err != nil {
		t.Fatal(err)
	}
	rules, err := d.ListSenderRules()
	if err != nil || len(rules) != 1 {
		t.Fatalf("ListSenderRules = %+v (err %v), want a single flipped rule", rules, err)
	}
	if rules[0].Action != SenderBlock {
		t.Errorf("action = %q, want it flipped to block", rules[0].Action)
	}
}

// TestSenderRuleDelete proves removal by pattern (case-insensitive) reports whether
// it matched.
func TestSenderRuleDelete(t *testing.T) {
	d := setupSenderAccess(t)
	if err := d.SetSenderRule("gone@example.com", SenderBlock); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.DeleteSenderRule("GONE@example.com"); err != nil || !ok {
		t.Fatalf("delete = ok %v err %v, want it matched", ok, err)
	}
	if ok, err := d.DeleteSenderRule("gone@example.com"); err != nil || ok {
		t.Fatalf("second delete = ok %v err %v, want no match", ok, err)
	}
}

// TestSenderRuleRejectsBadInput proves an empty pattern or unknown action is
// rejected rather than stored.
func TestSenderRuleRejectsBadInput(t *testing.T) {
	d := setupSenderAccess(t)
	if err := d.SetSenderRule("   ", SenderAllow); err == nil {
		t.Error("empty pattern should be rejected")
	}
	if err := d.SetSenderRule("a@example.com", "maybe"); err == nil {
		t.Error("unknown action should be rejected")
	}
	rules, err := d.ListSenderRules()
	mustNoErr(t, "list the rules after the refusals", err)
	wantEq(t, "rules stored by the refused writes", len(rules), 0)

	// A refused action leaves an existing rule as it was.
	mustNoErr(t, "set a rule", d.SetSenderRule("a@example.com", SenderBlock))
	wantErr(t, "an unknown action replaced the rule", d.SetSenderRule("a@example.com", "maybe"))
	rules, err = d.ListSenderRules()
	mustNoErr(t, "list the rules", err)
	if len(rules) != 1 || rules[0].Action != SenderBlock {
		t.Errorf("rules after the refused flip = %+v, want the one block rule", rules)
	}
}
