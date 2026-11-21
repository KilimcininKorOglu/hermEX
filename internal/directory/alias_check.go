package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// An alias is not merely a second inbound route. resolve() matches an address
// over three keys (users.username, altnames.altname, aliases.aliasname), and
// Identities() reports a user's aliases as the addresses they may send as, which
// both the webmail compose gate and the SMTP submission MAIL FROM check honour.
// So an unchecked alias row can silently break an account or hand it an identity
// it should not have, and neither shows up until mail stops working.
//
// These are the three ways that happened, each rejected before the row is
// written rather than discovered later:
var (
	// ErrAliasNotLocal rejects an alias in a domain this server does not host.
	// Mail can never arrive at it, and Identities would let the account originate
	// mail claiming an address in a domain the operator has no authority over.
	ErrAliasNotLocal = errors.New("directory: an alias must be in a domain this server hosts")
	// ErrAliasTargetUnknown rejects an alias to an address that is not an account.
	// aliases.mainname is matched against users.username, so anything else, a
	// second alias included, resolves to nothing.
	ErrAliasTargetUnknown = errors.New("directory: an alias must point at an existing account")
)

// rowQuerier is the subset of *sql.DB and *sql.Tx these checks need, so a
// validation can run inside the transaction that writes the rows.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// checkAliasTarget confirms main names an existing account.
func checkAliasTarget(q rowQuerier, main string) error {
	var one int
	err := q.QueryRow(`SELECT 1 FROM users WHERE username = ?`, main).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrAliasTargetUnknown, main)
	}
	return err
}

// checkAliasDomain confirms the alias is domain-qualified and that the domain is
// one this server hosts. It deliberately ignores domain_status: a suspended
// domain is still the operator's, and refusing here would stop them editing the
// aliases of an account while its domain is turned off.
func checkAliasDomain(q rowQuerier, alias string) error {
	at := strings.LastIndexByte(alias, '@')
	if at <= 0 || at == len(alias)-1 {
		return fmt.Errorf("%w: %s has no domain", ErrAliasNotLocal, alias)
	}
	var one int
	err := q.QueryRow(`SELECT 1 FROM domains WHERE domainname = ?`, alias[at+1:]).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrAliasNotLocal, alias)
	}
	return err
}

// checkAliasInOrg confirms the alias lies in the account's own domain or in another
// domain of the same organization. org_id 0 is "no organization", so a domain
// outside any organization reaches only itself, the bound GAL visibility follows.
func checkAliasInOrg(q rowQuerier, alias, main string) error {
	var one int
	err := q.QueryRow(`
SELECT 1 FROM domains a, domains m
 WHERE a.domainname = ? AND m.domainname = ?
   AND (a.id = m.id OR (a.org_id <> 0 AND a.org_id = m.org_id))`,
		AddressDomain(alias), AddressDomain(main)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s is outside the account's organization", ErrAliasNotLocal, alias)
	}
	return err
}

// checkAliasFree confirms the address is not already in use as an account, an
// alternative name, or another alias.
//
// A collision does not overwrite anything: resolve() reads all three keys in one
// UNION and treats two matches as no match at all, so binding an alias over an
// existing address silently makes BOTH unreachable. The account stops receiving
// mail and stops being able to sign in, with nothing to point at as the cause.
func checkAliasFree(q rowQuerier, alias string) error {
	var kind string
	err := q.QueryRow(`
SELECT 'an account' FROM users WHERE username = ?
UNION
SELECT 'an alternative name' FROM altnames WHERE altname = ?
UNION
SELECT 'another alias' FROM aliases WHERE aliasname = ?
LIMIT 1`, alias, alias, alias).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("directory: %s is already %s", alias, kind)
}

// checkAddressUnclaimed confirms a new account row's address is not already an
// alternative name or an alias. resolve() would then match the address twice and
// answer nothing, so the existing route and the new account would both go dark.
// Two account rows are kept apart by the username UNIQUE key, so that case keeps
// its own duplicate-key error.
func checkAddressUnclaimed(q rowQuerier, addr string) error {
	var kind string
	err := q.QueryRow(`
SELECT 'an alternative name' FROM altnames WHERE altname = ?
UNION
SELECT 'an alias' FROM aliases WHERE aliasname = ?
LIMIT 1`, addr, addr).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("directory: %s is already %s", addr, kind)
}

// checkAlias runs every alias precondition in order, cheapest and most specific
// first so the reported error names the actual problem. An alias is an inbound
// route and a send-as identity, so it must also lie in the account's own
// organization (checkAliasInOrg), whichever surface creates it: one in another
// customer's domain would take that customer's mail and let the account send
// under the address.
func checkAlias(q rowQuerier, alias, main string) error {
	if err := checkAliasDomain(q, alias); err != nil {
		return err
	}
	if err := checkAliasFree(q, alias); err != nil {
		return err
	}
	if err := checkAliasTarget(q, main); err != nil {
		return err
	}
	return checkAliasInOrg(q, alias, main)
}
