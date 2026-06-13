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
