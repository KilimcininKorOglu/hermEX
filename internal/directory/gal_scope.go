package directory

import "strings"

// The address book is scoped to the caller. Without a scope every authenticated
// account can enumerate every mailbox address in the deployment, which on a
// multi-tenant server means one tenant reading another tenant's staff list.
//
// The visible set is the caller's organization when their domain belongs to one,
// and the caller's own domain otherwise. domains.org_id 0 is the schema's
// reserved "organizationless" sentinel and is the value CreateDomain leaves, so
// treating it as a group would put every ungrouped domain in one bucket and
// reinstate the leak; the safe reading of "not grouped" is "no grouping".
//
// A deployment with a single domain is unaffected either way. A single company
// spanning two domains puts both in one organization (AssignDomainToOrg).
const galScopePredicate = ` AND ((? <> 0 AND d.org_id = ?) OR (? = 0 AND u.domain_id = ?))`

// galScope resolves the caller to the arguments galScopePredicate binds.
//
// It reports ok=false for an empty or unresolvable caller, and every scoped query
// then returns nothing rather than everything. That is the direction a mistake
// has to fail in: a surface that forgets to pass its authenticated user shows an
// empty address book, which is visible and reported, instead of quietly serving
// the whole deployment again.
func (d *SQLDirectory) galScope(caller string) (args []any, ok bool) {
	caller = strings.ToLower(strings.TrimSpace(caller))
	if caller == "" {
		return nil, false
	}
	row, found, err := d.resolve(caller)
	if err != nil || !found {
		return nil, false
	}
	return []any{row.orgID, row.orgID, row.orgID, row.domainID}, true
}
