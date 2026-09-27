// Package ldapsync runs a full LDAP/AD downsync, shared by the admin CLI
// (cmd/admin ldap-sync) and the admin panel's task worker so both apply the same
// profile, photo, and group settings. It orchestrates over interfaces;
// *ldapauth.Verifier and *directory.SQLDirectory satisfy them.
//
// A downsync runs per binding: one local domain bound to one connection. It
// creates users and distribution lists only in that domain, files contacts under
// it, and prunes only what that domain holds, so two domains bound to one
// directory never prune each other's entries.
package ldapsync

import (
	"errors"
	"fmt"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/ldapauth"
	"hermex/internal/objectstore"
)

// Syncer reads accounts, groups and mail contacts from the directory.
type Syncer interface {
	Sync(directory.LDAPConfig) ([]ldapauth.SyncedUser, error)
	SyncGroups(directory.LDAPConfig) ([]ldapauth.SyncedGroup, error)
	SyncContacts(directory.LDAPConfig) ([]ldapauth.SyncedContact, error)
}

// Store applies the downsync to the local directory.
type Store interface {
	UpsertLDAPUser(username string, externid []byte, maildir string) (bool, error)
	ApplyLDAPProfile(username string, values map[string]string) (bool, error)
	SyncAliasesFor(username string, aliases []string) (skipped []string, found bool, err error)
	UpsertLDAPGroup(listname string, externid []byte, owner string, members []string) (bool, error)
	ListMListsInDomain(domainID int64) ([]directory.MListInfo, error)
	DeleteMList(listname string) (bool, error)
	UpsertLDAPContact(email string, externid []byte, displayName, domain string) (bool, error)
	ListContactsInDomain(domainID int64) ([]directory.ContactInfo, error)
	DeleteLDAPContact(email string) (bool, error)
}

// Bindings resolves the effective configuration of a binding.
type Bindings interface {
	LDAPConfigForBinding(bindingID int64) (directory.LDAPConfig, bool, error)
}

// ErrNoDomain refuses a downsync whose configuration names no bound domain: without
// one it could neither scope what it creates nor what it prunes.
var ErrNoDomain = errors.New("ldapsync: the configuration is not bound to a domain")

// Run performs a full downsync of one binding: each account of the bound domain,
// its optional profile fields and portrait, and (when enabled) the directory's
// mail-bearing groups of the domain into LDAP-mastered distribution lists and its
// mail contacts into contacts filed under the domain. maildirFor maps a login to its
// mailbox path; logf records non-fatal per-entry problems (a skipped account, an
// unresolved member). It returns a one-line summary.
func Run(cfg directory.LDAPConfig, syncer Syncer, store Store, maildirFor func(string) string, logf func(string, ...any)) (string, error) {
	if cfg.DomainID == 0 || strings.TrimSpace(cfg.Domain) == "" {
		return "", ErrNoDomain
	}
	users, err := syncer.Sync(cfg)
	if err != nil {
		return "", err
	}
	res := syncUsers(users, cfg, store, maildirFor, logf)
	summary := fmt.Sprintf("Synced %d directory entries: %d created, %d updated, %d outside %s.",
		len(users), res.created, res.updated, res.foreign, cfg.Domain)

	groupSummary, err := runGroupPass(cfg, syncer, store, res.dnToEmail, logf)
	summary += groupSummary
	if err != nil {
		return summary, err
	}
	contactSummary, err := runContactPass(cfg, syncer, store, logf)
	return summary + contactSummary, err
}

// RunBindings runs Run for each binding in turn and returns one summary line per
// binding. A binding that fails does not stop the others; every failure is
// returned, joined.
func RunBindings(ids []int64, src Bindings, syncer Syncer, store Store, maildirFor func(string) string, logf func(string, ...any)) (string, error) {
	var lines []string
	var errs []error
	for _, id := range ids {
		line, err := runBinding(id, src, syncer, store, maildirFor, logf)
		if line != "" {
			lines = append(lines, line)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("binding %d: %w", id, err))
		}
	}
	return strings.Join(lines, " "), errors.Join(errs...)
}

// runBinding resolves one binding and syncs it, prefixing the summary with its domain.
func runBinding(id int64, src Bindings, syncer Syncer, store Store, maildirFor func(string) string, logf func(string, ...any)) (string, error) {
	cfg, ok, err := src.LDAPConfigForBinding(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("no such binding")
	}
	summary, err := Run(cfg, syncer, store, maildirFor, logf)
	if summary == "" {
		return "", err
	}
	return cfg.Domain + ": " + summary, err
}

// inDomain reports whether an address belongs to the domain.
func inDomain(addr, domain string) bool {
	at := strings.LastIndexByte(addr, '@')
	return at >= 0 && strings.EqualFold(addr[at+1:], domain)
}

// userResult is the outcome of the user pass.
type userResult struct {
	dnToEmail                 map[string]string
	created, updated, foreign int
}

// syncUsers applies each account of the bound domain to the local directory,
// returning the DN-to-login map the group pass resolves members through and the
// counts. An account in another domain is not created or updated, but its DN is
// still recorded, so a group of this domain can list it as a member.
func syncUsers(users []ldapauth.SyncedUser, cfg directory.LDAPConfig, store Store, maildirFor func(string) string, logf func(string, ...any)) userResult {
	res := userResult{dnToEmail: make(map[string]string, len(users))}
	for _, u := range users {
		if !inDomain(u.Username, cfg.Domain) {
			res.foreign++
			res.recordDN(u)
			continue
		}
		maildir := maildirFor(u.Username)
		isNew, err := store.UpsertLDAPUser(u.Username, u.ExternID, maildir)
		if err != nil {
			logf("skip %s: %v", u.Username, err)
			continue
		}
		if isNew {
			res.created++
		} else {
			res.updated++
		}
		res.recordDN(u)
		applyProfile(u, maildir, store, logf)
		applyAliases(u, cfg, store, logf)
	}
	return res
}

// recordDN remembers an account's DN for group member resolution.
func (r userResult) recordDN(u ldapauth.SyncedUser) {
	if u.DN != "" {
		r.dnToEmail[strings.ToLower(u.DN)] = u.Username
	}
}

// runGroupPass syncs the directory's groups when group sync is enabled, returning the
// summary fragment to append. An empty fragment means the pass did not run.
func runGroupPass(cfg directory.LDAPConfig, syncer Syncer, store Store, dnToEmail map[string]string, logf func(string, ...any)) (string, error) {
	if !cfg.SyncGroups {
		return "", nil
	}
	groups, err := syncer.SyncGroups(cfg)
	if err != nil {
		return "", err
	}
	res := syncGroups(groups, cfg.Domain, dnToEmail, store, logf)
	pruned, err := pruneMastered(res.synced, cfg.DomainID, store, logf)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(" Groups: %d created, %d updated, %d pruned, %d outside %s (of %d).",
		res.created, res.updated, pruned, res.foreign, cfg.Domain, len(groups)), nil
}

// runContactPass syncs the directory's mail contacts when contact sync is enabled,
// filing them under the bound domain, and returns the summary fragment to append.
func runContactPass(cfg directory.LDAPConfig, syncer Syncer, store Store, logf func(string, ...any)) (string, error) {
	if !cfg.SyncContacts {
		return "", nil
	}
	contacts, err := syncer.SyncContacts(cfg)
	if err != nil {
		return "", err
	}
	synced, created, updated := syncContacts(contacts, cfg.Domain, store, logf)
	pruned, err := pruneContacts(synced, cfg.DomainID, store, logf)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(" Contacts: %d created, %d updated, %d pruned (of %d).", created, updated, pruned, len(contacts)), nil
}

// applyAliases replaces the account's aliases with the ones the directory publishes, after
// the upsert so the account exists to alias. An alias in another local domain is kept:
// only accounts, lists and contact filing are scoped to the bound domain. It runs only
// while an alias attribute is configured, because a disabled alias sync must never remove
// an alias an operator added by hand. An address the directory owns but this server cannot
// bind is skipped and reported, not fatal: the account itself is already synced.
func applyAliases(u ldapauth.SyncedUser, cfg directory.LDAPConfig, store Store, logf func(string, ...any)) {
	if cfg.AliasAttr == "" {
		return
	}
	skipped, _, err := store.SyncAliasesFor(u.Username, u.Aliases)
	if err != nil {
		logf("%s aliases: %v", u.Username, err)
		return
	}
	for _, a := range skipped {
		logf("%s alias %s skipped: not a local address, or already in use", u.Username, a)
	}
}

// applyProfile writes the profile string fields into the directory and the portrait
// into the mailbox store (after the upsert, so the maildir exists). Either failing is
// logged, not fatal: the account itself is already synced.
func applyProfile(u ldapauth.SyncedUser, maildir string, store Store, logf func(string, ...any)) {
	if len(u.Fields) > 0 {
		if _, err := store.ApplyLDAPProfile(u.Username, u.Fields); err != nil {
			logf("%s profile: %v", u.Username, err)
		}
	}
	if len(u.Photo) == 0 || maildir == "" {
		return
	}
	st, err := objectstore.Open(maildir)
	if err != nil {
		logf("%s photo: %v", u.Username, err)
		return
	}
	if err := st.SetUserPhoto(u.Photo); err != nil {
		logf("%s photo: %v", u.Username, err)
	}
	if err := st.Close(); err != nil {
		logf("%s photo: %v", u.Username, err)
	}
}

// groupResult is the outcome of the group pass.
type groupResult struct {
	synced                    map[string]bool
	created, updated, foreign int
}

// syncGroups applies the directory's mail-bearing groups of the bound domain as
// LDAP-mastered lists, returning the set of list names it synced and the counts. A
// group whose address is in another domain is skipped and counted.
func syncGroups(groups []ldapauth.SyncedGroup, domain string, dnToEmail map[string]string, store Store, logf func(string, ...any)) groupResult {
	res := groupResult{synced: make(map[string]bool, len(groups))}
	for _, g := range groups {
		if !inDomain(g.Mail, domain) {
			res.foreign++
			continue
		}
		owner := dnToEmail[strings.ToLower(g.OwnerDN)] // "" if none/unresolved
		members := resolveMembers(g, dnToEmail, logf)
		isNew, err := store.UpsertLDAPGroup(g.Mail, []byte(strings.ToLower(g.Mail)), owner, members)
		if err != nil {
			logf("skip group %s: %v", g.Mail, err)
			continue
		}
		res.synced[strings.ToLower(g.Mail)] = true
		if isNew {
			res.created++
		} else {
			res.updated++
		}
	}
	return res
}

// resolveMembers maps a group's member DNs to synced logins, reporting each member
// the downsync did not bring in.
func resolveMembers(g ldapauth.SyncedGroup, dnToEmail map[string]string, logf func(string, ...any)) []string {
	members := make([]string, 0, len(g.MemberDNs))
	for _, mdn := range g.MemberDNs {
		email := dnToEmail[strings.ToLower(mdn)]
		if email == "" {
			logf("group %s: member %q is not a synced user, skipped", g.Mail, mdn)
			continue
		}
		members = append(members, email)
	}
	return members
}

// syncContacts applies the directory's mail contacts as LDAP-mastered contacts filed
// under the domain, returning the set of addresses it synced and the created/updated
// counts. An address the local directory already holds as something else (a mailbox
// user, a list, another domain's contact) is logged and skipped rather than converted.
func syncContacts(contacts []ldapauth.SyncedContact, domain string, store Store, logf func(string, ...any)) (synced map[string]bool, created, updated int) {
	synced = make(map[string]bool, len(contacts))
	for _, c := range contacts {
		isNew, err := store.UpsertLDAPContact(c.Mail, c.ExternID, c.DisplayName, domain)
		if err != nil {
			logf("skip contact %s: %v", c.Mail, err)
			continue
		}
		synced[strings.ToLower(c.Mail)] = true
		if isNew {
			created++
		} else {
			updated++
		}
	}
	return synced, created, updated
}

// pruneContacts deletes the domain's mastered contacts no longer present in the
// directory. A contact with no LDAP id was made by hand and is never pruned. A failed
// delete is logged and the pass goes on.
func pruneContacts(synced map[string]bool, domainID int64, store Store, logf func(string, ...any)) (int, error) {
	contacts, err := store.ListContactsInDomain(domainID)
	if err != nil {
		return 0, fmt.Errorf("list the domain's contacts: %w", err)
	}
	pruned := 0
	for _, c := range contacts {
		if c.LDAPID == "" || synced[strings.ToLower(c.Address)] {
			continue
		}
		removed, err := store.DeleteLDAPContact(c.Address)
		if err != nil {
			logf("prune contact %s: %v", c.Address, err)
			continue
		}
		if removed {
			pruned++
		}
	}
	return pruned, nil
}

// pruneMastered deletes the domain's mastered lists no longer present in the
// directory. A failed delete is logged and the pass goes on.
func pruneMastered(synced map[string]bool, domainID int64, store Store, logf func(string, ...any)) (int, error) {
	lists, err := store.ListMListsInDomain(domainID)
	if err != nil {
		return 0, fmt.Errorf("list the domain's lists: %w", err)
	}
	pruned := 0
	for _, l := range lists {
		if !l.LDAPMastered || synced[strings.ToLower(l.Listname)] {
			continue
		}
		removed, err := store.DeleteMList(l.Listname)
		if err != nil {
			logf("prune group %s: %v", l.Listname, err)
			continue
		}
		if removed {
			pruned++
		}
	}
	return pruned, nil
}
