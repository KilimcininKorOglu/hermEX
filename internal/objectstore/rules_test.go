package objectstore

import (
	"errors"
	"testing"

	"hermex/internal/mapi"
)

// subjectContainsMarkRead builds a faithful "if the subject contains needle,
// mark the message read" rule: a ResContent restriction on PR_SUBJECT paired
// with a single OpMarkAsRead action block.
func subjectContainsMarkRead(name, needle string) Rule {
	return Rule{
		FolderID: int64(mapi.PrivateFIDInbox),
		Name:     name,
		State:    mapi.RuleStateEnabled,
		Condition: mapi.Restriction{
			Type: mapi.ResContent,
			Value: mapi.ContentRestriction{
				// substring, case-insensitive (FL_SUBSTRING | FL_IGNORECASE)
				FuzzyLevel: 0x00010001,
				PropTag:    mapi.PrSubject,
				PropVal:    mapi.TaggedPropVal{Tag: mapi.PrSubject, Value: needle},
			},
		},
		Actions: mapi.RuleActions{Blocks: []mapi.ActionBlock{{Type: mapi.OpMarkAsRead}}},
	}
}

// TestRuleRoundTrip stores a rule and reads it back, asserting every meaningful
// field survives the RESTRICTION / RULE_ACTIONS serialization: the condition's
// proptag and search string, the action type, the enabled state, and the name.
// A rule that does not round-trip faithfully would silently never fire (or fire
// on the wrong message), so structural fidelity is the property under test.
func TestRuleRoundTrip(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)

	id, err := s.AddRule(subjectContainsMarkRead("auto-read invoices", "invoice"))
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if id <= 0 {
		t.Fatalf("AddRule returned a non-positive id %d", id)
	}

	rules, err := s.ListRules(inbox)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("ListRules returned %d rules, want 1", len(rules))
	}
	r := rules[0]
	if r.ID != id {
		t.Errorf("rule id = %d, want %d", r.ID, id)
	}
	if r.Name != "auto-read invoices" {
		t.Errorf("name = %q, want %q", r.Name, "auto-read invoices")
	}
	if !r.Enabled() {
		t.Errorf("rule should be enabled (state = %#x)", r.State)
	}
	if r.Sequence != 1 {
		t.Errorf("first rule sequence = %d, want 1", r.Sequence)
	}
	if r.Provider != ruleProviderDefault {
		t.Errorf("provider = %q, want default %q", r.Provider, ruleProviderDefault)
	}

	// Condition fidelity: ResContent on PR_SUBJECT carrying the search string.
	if r.Condition.Type != mapi.ResContent {
		t.Fatalf("condition type = %d, want ResContent", r.Condition.Type)
	}
	cr, ok := r.Condition.Value.(mapi.ContentRestriction)
	if !ok {
		t.Fatalf("condition value is %T, want ContentRestriction", r.Condition.Value)
	}
	if cr.PropTag != mapi.PrSubject {
		t.Errorf("condition proptag = %#x, want PR_SUBJECT %#x", cr.PropTag, mapi.PrSubject)
	}
	if cr.FuzzyLevel != 0x00010001 {
		t.Errorf("fuzzy level = %#x, want 0x00010001", cr.FuzzyLevel)
	}
	if got, _ := cr.PropVal.Value.(string); got != "invoice" {
		t.Errorf("condition search value = %q, want %q", got, "invoice")
	}

	// Action fidelity: exactly one OpMarkAsRead block.
	if len(r.Actions.Blocks) != 1 {
		t.Fatalf("rule has %d action blocks, want 1", len(r.Actions.Blocks))
	}
	if r.Actions.Blocks[0].Type != mapi.OpMarkAsRead {
		t.Errorf("action type = %#x, want OpMarkAsRead %#x", r.Actions.Blocks[0].Type, mapi.OpMarkAsRead)
	}
}

// TestRuleSequenceAutoAppend checks that rules added with a non-positive
// sequence are appended after the folder's current highest sequence, so
// ListRules returns them in creation order.
func TestRuleSequenceAutoAppend(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)

	id1, err := s.AddRule(subjectContainsMarkRead("first", "a"))
	if err != nil {
		t.Fatalf("AddRule first: %v", err)
	}
	id2, err := s.AddRule(subjectContainsMarkRead("second", "b"))
	if err != nil {
		t.Fatalf("AddRule second: %v", err)
	}

	rules, err := s.ListRules(inbox)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("ListRules returned %d rules, want 2", len(rules))
	}
	if rules[0].ID != id1 || rules[1].ID != id2 {
		t.Errorf("rules out of creation order: got ids %d,%d want %d,%d",
			rules[0].ID, rules[1].ID, id1, id2)
	}
	if rules[0].Sequence != 1 || rules[1].Sequence != 2 {
		t.Errorf("sequences = %d,%d want 1,2", rules[0].Sequence, rules[1].Sequence)
	}
}

// TestDeleteRule verifies a rule is removed by id, leaving the folder's other
// rules intact, and that deleting a missing rule reports ErrNotFound.
func TestDeleteRule(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)

	id1, err := s.AddRule(subjectContainsMarkRead("keep", "keep"))
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	id2, err := s.AddRule(subjectContainsMarkRead("drop", "drop"))
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	if err := s.DeleteRule(id2); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	rules, err := s.ListRules(inbox)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != id1 {
		t.Fatalf("after delete, rules = %+v, want only id %d", rules, id1)
	}

	if err := s.DeleteRule(id2); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteRule of a missing rule = %v, want ErrNotFound", err)
	}
}

// TestSetRuleEnabled checks the enable/disable toggle flips only ST_ENABLED,
// preserves the rule's other state bits, and reports ErrNotFound for a missing
// rule.
func TestSetRuleEnabled(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)

	// Start enabled, with an extra state bit set to prove it is preserved.
	r := subjectContainsMarkRead("toggle me", "x")
	r.State = mapi.RuleStateEnabled | mapi.RuleStateExitLevel
	id, err := s.AddRule(r)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetRuleEnabled(id, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	rules, _ := s.ListRules(inbox)
	if rules[0].Enabled() {
		t.Errorf("rule still enabled after disable")
	}
	if rules[0].State&mapi.RuleStateExitLevel == 0 {
		t.Errorf("disable cleared an unrelated state bit (ST_EXIT_LEVEL)")
	}

	if err := s.SetRuleEnabled(id, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	rules, _ = s.ListRules(inbox)
	if !rules[0].Enabled() {
		t.Errorf("rule not enabled after enable")
	}

	if err := s.SetRuleEnabled(999999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRuleEnabled on a missing rule = %v, want ErrNotFound", err)
	}
}

// TestListRulesEmpty checks a folder with no rules yields an empty slice and no
// error (not a nil-dereference or a spurious row).
func TestListRulesEmpty(t *testing.T) {
	s := openSeededStore(t)
	rules, err := s.ListRules(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("empty folder returned %d rules, want 0", len(rules))
	}
}

// fullPatch is the RopModifyRules Add patch a client sends for a complete new rule —
// every modelled column present, the way Outlook's Rules manager writes an add.
func fullPatch(r Rule) RulePatch {
	return RulePatch{Name: &r.Name, State: &r.State, Condition: &r.Condition, Actions: &r.Actions}
}

// TestModifyRulesAdd stores a rule through the ModifyRules batch (the ROP entry) and
// reads it back, confirming an Add patch lands as a real rule.
func TestModifyRulesAdd(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	r := subjectContainsMarkRead("via-modify", "receipt")
	if err := s.ModifyRules(inbox, false, []RuleChange{{Op: RuleAdd, Patch: fullPatch(r)}}); err != nil {
		t.Fatalf("ModifyRules add: %v", err)
	}
	rules, err := s.ListRules(inbox)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 || rules[0].Name != "via-modify" {
		t.Fatalf("ListRules = %d rules (first name %q), want 1 named via-modify", len(rules), firstRuleName(rules))
	}
}

// TestModifyRulesPresentOnlyMerge is the load-bearing test for the ROW_MODIFY
// semantics: a Modify that carries ONLY PR_RULE_STATE must update the state and leave
// the condition and actions UNTOUCHED. A naive replace-all update (rewriting every
// column from a zero-valued struct) would wipe the omitted condition/actions and the
// rule would silently stop matching — the exact bug the reference's per-column
// present-only merge avoids. This test fails against a replace-all implementation.
func TestModifyRulesPresentOnlyMerge(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	id, err := s.AddRule(subjectContainsMarkRead("invoices", "invoice"))
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	disabled := uint32(0) // a state-only modify clearing ST_ENABLED
	if err := s.ModifyRules(inbox, false, []RuleChange{{
		Op: RuleModify, RuleID: id, Patch: RulePatch{State: &disabled},
	}}); err != nil {
		t.Fatalf("ModifyRules modify: %v", err)
	}

	rules, err := s.ListRules(inbox)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(rules))
	}
	got := rules[0]
	if got.State != 0 {
		t.Errorf("State = %#x, want 0 (the state-only modify must apply)", got.State)
	}
	if got.Condition.Type != mapi.ResContent {
		t.Errorf("condition wiped by a state-only modify: Type = %d, want ResContent", got.Condition.Type)
	}
	if len(got.Actions.Blocks) != 1 || got.Actions.Blocks[0].Type != mapi.OpMarkAsRead {
		t.Errorf("actions wiped by a state-only modify: %+v", got.Actions)
	}
}

// TestModifyRulesRemove drops a rule by its id through the batch.
func TestModifyRulesRemove(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	id, err := s.AddRule(subjectContainsMarkRead("doomed", "x"))
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := s.ModifyRules(inbox, false, []RuleChange{{Op: RuleRemove, RuleID: id}}); err != nil {
		t.Fatalf("ModifyRules remove: %v", err)
	}
	rules, _ := s.ListRules(inbox)
	if len(rules) != 0 {
		t.Errorf("got %d rules after remove, want 0", len(rules))
	}
}

// TestModifyRulesReplace verifies the REPLACE flag clears the folder's existing rules
// before applying the batch's adds.
func TestModifyRulesReplace(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	if _, err := s.AddRule(subjectContainsMarkRead("old1", "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRule(subjectContainsMarkRead("old2", "b")); err != nil {
		t.Fatal(err)
	}

	fresh := subjectContainsMarkRead("fresh", "c")
	if err := s.ModifyRules(inbox, true, []RuleChange{{Op: RuleAdd, Patch: fullPatch(fresh)}}); err != nil {
		t.Fatalf("ModifyRules replace: %v", err)
	}
	rules, _ := s.ListRules(inbox)
	if len(rules) != 1 || rules[0].Name != "fresh" {
		t.Errorf("after replace got %d rules (first %q), want just [fresh]", len(rules), firstRuleName(rules))
	}
}

// TestModifyRulesForeignRuleNoOp verifies a Modify keyed to a rule that lives on a
// different folder is a silent no-op — it must never reach across folders to edit
// another folder's rule (the ownership guard).
func TestModifyRulesForeignRuleNoOp(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	sent := int64(mapi.PrivateFIDSentItems)
	id, err := s.AddRule(subjectContainsMarkRead("inbox-rule", "x")) // lands on Inbox
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	cleared := uint32(0)
	if err := s.ModifyRules(sent, false, []RuleChange{{Op: RuleModify, RuleID: id, Patch: RulePatch{State: &cleared}}}); err != nil {
		t.Fatalf("ModifyRules: %v", err)
	}
	rules, _ := s.ListRules(inbox)
	if len(rules) != 1 || rules[0].State != mapi.RuleStateEnabled {
		t.Errorf("a Sent-folder modify altered the Inbox rule: state = %#x, want unchanged ST_ENABLED", rules[0].State)
	}
}

// firstRuleName is a small helper for failure messages.
func firstRuleName(rules []Rule) string {
	if len(rules) == 0 {
		return ""
	}
	return rules[0].Name
}

// TestSetRuleSequence reorders two rules by swapping their sequences and reports
// ErrNotFound for an unknown rule.
func TestSetRuleSequence(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	mk := func(name string) int64 {
		id, err := s.AddRule(Rule{
			FolderID: inbox, Name: name, State: mapi.RuleStateEnabled,
			Condition: RuleSubjectContains(name),
			Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMarkReadAction()}},
		})
		if err != nil {
			t.Fatalf("AddRule %s: %v", name, err)
		}
		return id
	}
	a := mk("a")
	b := mk("b")

	rules, err := s.ListRules(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if rules[0].ID != a {
		t.Fatalf("rule a should be first, got %d", rules[0].ID)
	}
	if err := s.SetRuleSequence(a, rules[1].Sequence); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRuleSequence(b, rules[0].Sequence); err != nil {
		t.Fatal(err)
	}
	if rules, _ = s.ListRules(inbox); rules[0].ID != b {
		t.Errorf("after swap rule b should be first, got %d", rules[0].ID)
	}
	if err := s.SetRuleSequence(999999, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRuleSequence on unknown id = %v, want ErrNotFound", err)
	}
}
