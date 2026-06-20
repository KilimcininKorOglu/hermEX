package mta

import (
	"strings"

	"hermex/internal/directory"
)

// maxListDepth caps nested distribution-list expansion. It is a backstop only —
// the seen-set already breaks cycles — so it is set well above any sane nesting.
const maxListDepth = 20

// MListExpander is the directory capability the MTA needs to expand distribution
// lists: it resolves a list address to its direct members under the sender's
// posting privilege. *directory.SQLDirectory satisfies it; a directory that does
// not (the static test accounts) simply means lists are unsupported and a list
// address is routed like any other recipient.
type MListExpander interface {
	ExpandMList(listAddr, from string) ([]string, directory.MListResult, error)
}

// expandMailingList resolves a recipient address to its final non-list member
// addresses, recursively expanding nested lists. from is the original sender,
// re-checked at every level — a nested list that refuses the sender contributes
// nothing. The seen-set breaks membership cycles and de-duplicates recipients, so
// each address appears at most once however the lists are wired.
//
// isList reports whether `to` was a distribution list at all; res carries the
// top-level posting result so the caller can accept, refuse, or fall through to
// ordinary recipient routing.
func expandMailingList(exp MListExpander, from, to string) (leaves []string, isList bool, res directory.MListResult, err error) {
	members, res, err := exp.ExpandMList(to, from)
	if err != nil {
		return nil, false, res, err
	}
	switch res {
	case directory.MListNone:
		return nil, false, res, nil
	case directory.MListOK:
		// fall through to the recursive expansion below
	default:
		return nil, true, res, nil // a list, but posting is refused
	}

	seen := map[string]bool{strings.ToLower(strings.TrimSpace(to)): true}
	var collect func(addrs []string, depth int)
	collect = func(addrs []string, depth int) {
		for _, m := range addrs {
			key := strings.ToLower(strings.TrimSpace(m))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			if depth >= maxListDepth {
				leaves = append(leaves, m) // refuse to recurse further; treat as a leaf
				continue
			}
			sub, subRes, subErr := exp.ExpandMList(m, from)
			switch {
			case subErr != nil || subRes == directory.MListNone:
				leaves = append(leaves, m) // an ordinary recipient
			case subRes == directory.MListOK:
				collect(sub, depth+1)
			default:
				// a nested list that refuses this sender contributes nothing
			}
		}
	}
	collect(members, 1)
	return leaves, true, directory.MListOK, nil
}

// expandRecipientList expands every distribution list in a recipient set into its
// member addresses, returning the flattened non-list recipients plus the list
// addresses whose posting privilege refused the sender. A directory that cannot
// expand lists returns the recipients unchanged.
func expandRecipientList(accounts directory.Accounts, from string, recipients []string) (leaves, refused []string) {
	exp, ok := accounts.(MListExpander)
	if !ok {
		return recipients, nil
	}
	for _, r := range recipients {
		ls, isList, res, err := expandMailingList(exp, from, r)
		switch {
		case err != nil || !isList:
			leaves = append(leaves, r)
		case res != directory.MListOK:
			refused = append(refused, r)
		default:
			leaves = append(leaves, ls...)
		}
	}
	return leaves, refused
}
