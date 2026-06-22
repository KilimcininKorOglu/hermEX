package antispam

import "strings"

// Operator allow/block actions. The string values are the contract shared with the
// directory store; an allowlisted sender is rescued from score-based junking (a
// hard DMARC failure still wins), a blocklisted sender is always spam.
const (
	AccessAllow = "allow"
	AccessBlock = "block"
)

// AccessList is the operator's allow/block rules, consulted by Score to override a
// verdict. Patterns are lowercased email addresses or bare domains; an exact-address
// rule beats a domain rule (most specific wins). It is immutable once built, so the
// MTA hot-swaps a whole new list rather than mutating one in place.
type AccessList struct {
	rules map[string]string // lowercased pattern -> AccessAllow | AccessBlock
}

// NewAccessList builds an AccessList from pattern→action pairs. Patterns are
// lowercased and trimmed; the caller is expected to have validated the actions
// (the directory store rejects anything but allow/block).
func NewAccessList(rules map[string]string) *AccessList {
	m := make(map[string]string, len(rules))
	for p, a := range rules {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			m[p] = a
		}
	}
	return &AccessList{rules: m}
}

// Action returns the rule that applies to a sender — AccessAllow, AccessBlock, or
// "" for none. An exact email-address rule wins over a domain rule (most specific
// first); at the domain tier the envelope domain is tried before the From-header
// domain. An empty mailFrom (a bounce) matches nothing.
func (a *AccessList) Action(mailFrom, fromDomain string) string {
	if a == nil || mailFrom == "" {
		return ""
	}
	email := strings.ToLower(strings.TrimSpace(mailFrom))
	if act, ok := a.rules[email]; ok {
		return act // exact address — most specific
	}
	if d := domainOf(email); d != "" {
		if act, ok := a.rules[d]; ok {
			return act
		}
	}
	if fromDomain != "" {
		if act, ok := a.rules[strings.ToLower(strings.TrimSpace(fromDomain))]; ok {
			return act
		}
	}
	return ""
}
