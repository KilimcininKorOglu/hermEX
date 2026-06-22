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
	// ORDER BY action, pattern: "block" sorts before "allow"? No — alphabetical:
	// allow < block, so the allow row leads.
	if rules[0].Pattern != "partner@example.com" || rules[0].Action != SenderAllow {
		t.Errorf("rule[0] = %+v, want the normalised allow rule", rules[0])
	}
	if rules[1].Pattern != "spammy.example" || rules[1].Action != SenderBlock {
		t.Errorf("rule[1] = %+v, want the block rule", rules[1])
	}
}

// TestSenderRuleFlip proves re-adding a pattern with the other action flips it in
// place (one row, not two) — so a pattern always carries exactly one action.
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
	if rules, _ := d.ListSenderRules(); len(rules) != 0 {
		t.Errorf("nothing should have been stored, got %+v", rules)
	}
}
