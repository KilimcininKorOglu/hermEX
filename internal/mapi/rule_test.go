package mapi

import "testing"

// TestRuleProptagTypes pins the property TYPE of each rule proptag, not just its raw
// hex. The type nibble is what drives the wire value decoder: PrRuleCondition must be
// PtRestriction and PrRuleActions PtActions, or RopModifyRules would decode a rule's
// condition/actions with the wrong codec and the rule would not round-trip. A
// type-nibble typo in the constant is the impactful, plausible error this catches;
// the ids themselves are verified end-to-end by the G.Inc 1 RopModifyRules round-trip.
func TestRuleProptagTypes(t *testing.T) {
	cases := []struct {
		name string
		tag  PropTag
		want PropType
	}{
		{"PrRuleID", PrRuleID, PtI8},
		{"PrRuleSequence", PrRuleSequence, PtLong},
		{"PrRuleState", PrRuleState, PtLong},
		{"PrRuleUserFlags", PrRuleUserFlags, PtLong},
		{"PrRuleCondition", PrRuleCondition, PtRestriction},
		{"PrRuleActions", PrRuleActions, PtActions},
		{"PrRuleProvider", PrRuleProvider, PtUnicode},
		{"PrRuleProviderA", PrRuleProviderA, PtString8},
		{"PrRuleName", PrRuleName, PtUnicode},
		{"PrRuleNameA", PrRuleNameA, PtString8},
		{"PrRuleLevel", PrRuleLevel, PtLong},
		{"PrRuleProviderData", PrRuleProviderData, PtBinary},
	}
	for _, c := range cases {
		if got := c.tag.Type(); got != c.want {
			t.Errorf("%s.Type() = %#04x, want %#04x", c.name, uint16(got), uint16(c.want))
		}
	}
}
