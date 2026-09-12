package objectstore

import (
	"testing"

	"hermex/internal/mapi"
)

// string8Tag is the ANSI sibling of a unicode property tag, what an ANSI client
// writes and what a FastTransfer upload carrying a code-page string stores.
func string8Tag(tag mapi.PropTag) mapi.PropTag {
	return tag.WithType(mapi.PtString8)
}

// TestContentRuleMatchesAcrossStringTypes pins that a content condition reads a
// property stored under either string type. The rule editor writes unicode tags
// while a message property can reach the store as PT_STRING8, and a condition
// that insists on the exact type silently never fires.
func TestContentRuleMatchesAcrossStringTypes(t *testing.T) {
	stored := mapi.PropertyValues{{Tag: string8Tag(mapi.PrSubject), Value: "quarterly report"}}
	if !evalRestriction(RuleSubjectContains("quarterly"), stored) {
		t.Error("unicode content rule did not match a string8 subject")
	}

	unicode := mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "quarterly report"}}
	if !evalRestriction(contentContains(string8Tag(mapi.PrSubject), "quarterly"), unicode) {
		t.Error("string8 content rule did not match a unicode subject")
	}
}

// TestPropertyRuleMatchesAcrossStringTypes is the same guarantee for the
// relational restriction kind.
func TestPropertyRuleMatchesAcrossStringTypes(t *testing.T) {
	stored := mapi.PropertyValues{{Tag: string8Tag(mapi.PrSubject), Value: "report"}}
	r := mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop:   mapi.RelopEQ,
		PropTag: mapi.PrSubject,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrSubject, Value: "report"},
	}}
	if !evalRestriction(r, stored) {
		t.Error("unicode property rule did not match a string8 subject")
	}
}

// TestExistRuleMatchesAcrossStringTypes keeps the existence test consistent with
// the two that read the value: a property present under the sibling string type
// is present.
func TestExistRuleMatchesAcrossStringTypes(t *testing.T) {
	stored := mapi.PropertyValues{{Tag: string8Tag(mapi.PrSubject), Value: "report"}}
	r := mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{PropTag: mapi.PrSubject}}
	if !evalRestriction(r, stored) {
		t.Error("unicode exist rule did not see a string8 subject")
	}
}

// TestRuleDoesNotMatchAnUnrelatedProperty confirms the fallback did not widen
// matching beyond the two string types: an absent property still fails.
func TestRuleDoesNotMatchAnUnrelatedProperty(t *testing.T) {
	stored := mapi.PropertyValues{{Tag: string8Tag(mapi.PrBody), Value: "quarterly report"}}
	if evalRestriction(RuleSubjectContains("quarterly"), stored) {
		t.Error("a subject rule matched a message that carries only a body")
	}
}
