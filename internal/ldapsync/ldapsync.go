// Package ldapsync runs a full LDAP/AD downsync, shared by the admin CLI
// (cmd/admin ldap-sync) and the admin panel's task worker so both apply the same
// profile, photo, and group settings. It orchestrates over interfaces;
// *ldapauth.Verifier and *directory.SQLDirectory satisfy them.
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
	ListMLists() ([]directory.MListInfo, error)
	DeleteMList(listname string) (bool, error)
	UpsertLDAPContact(email string, externid []byte, displayName, domain string) (bool, error)
	ListContacts() ([]directory.ContactInfo, error)
	DeleteLDAPContact(email string) (bool, error)
}

// Run performs a full downsync: each account's existence, optional profile fields
// and portrait, and (when enabled) the directory's mail-bearing groups into
// LDAP-mastered distribution lists and its mail contacts into org mail contacts.
// maildirFor maps a login to its mailbox path; logf records non-fatal per-entry
// problems (a skipped account, an unresolved member). It returns a one-line summary.
func Run(cfg directory.LDAPConfig, syncer Syncer, store Store, maildirFor func(string) string, logf func(string, ...any)) (string, error) {
	users, err := syncer.Sync(cfg)
	if err != nil {
		return "", err
	}
	dnToEmail, created, updated := syncUsers(users, cfg, store, maildirFor, logf)
	summary := fmt.Sprintf("Synced %d directory entries: %d created, %d updated.", len(users), created, updated)

	groupSummary, err := runGroupPass(cfg, syncer, store, dnToEmail, logf)
	summary += groupSummary
	if err != nil {
		return summary, err
	}
	contactSummary, err := runContactPass(cfg, syncer, store, logf)
	return summary + contactSummary, err
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
	synced, created, updated := syncGroups(groups, dnToEmail, store, logf)
	pruned := pruneMastered(synced, store)
	return fmt.Sprintf(" Groups: %d created, %d updated, %d pruned (of %d).", created, updated, pruned, len(groups)), nil
}

// runContactPass syncs the directory's mail contacts when contact sync is enabled,
// returning the summary fragment to append.
func runContactPass(cfg directory.LDAPConfig, syncer Syncer, store Store, logf func(string, ...any)) (string, error) {
	if !cfg.SyncContacts {
		return "", nil
	}
	if strings.TrimSpace(cfg.ContactDomain) == "" {
		return "", errors.New("ldapsync: contact sync needs a filing domain")
	}
	contacts, err := syncer.SyncContacts(cfg)
	if err != nil {
		return "", err
	}
	synced, created, updated := syncContacts(contacts, cfg.ContactDomain, store, logf)
	pruned := pruneContacts(synced, store)
	return fmt.Sprintf(" Contacts: %d created, %d updated, %d pruned (of %d).", created, updated, pruned, len(contacts)), nil
}

// syncUsers applies each account to the local directory, returning the DN-to-login
// map the group pass resolves members through, and the created/updated counts.
func syncUsers(users []ldapauth.SyncedUser, cfg directory.LDAPConfig, store Store, maildirFor func(string) string, logf func(string, ...any)) (dnToEmail map[string]string, created, updated int) {
	dnToEmail = make(map[string]string, len(users))
	for _, u := range users {
		maildir := maildirFor(u.Username)
		isNew, err := store.UpsertLDAPUser(u.Username, u.ExternID, maildir)
		if err != nil {
			logf("skip %s: %v", u.Username, err)
			continue
		}
		if isNew {
			created++
		} else {
			updated++
		}
		if u.DN != "" {
			dnToEmail[strings.ToLower(u.DN)] = u.Username
		}
		applyProfile(u, maildir, store, logf)
		applyAliases(u, cfg, store, logf)
	}
	return dnToEmail, created, updated
}

// applyAliases replaces the account's aliases with the ones the directory publishes, after
// the upsert so the account exists to alias. It runs only while an alias attribute is
// configured, because a disabled alias sync must never remove an alias an operator added by
// hand. An address the directory owns but this server cannot bind is skipped and reported,
// not fatal: the account itself is already synced.
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
	_ = st.Close()
}

// syncGroups applies the directory's mail-bearing groups as LDAP-mastered lists,
// returning the set of list names it synced and the created/updated counts.
func syncGroups(groups []ldapauth.SyncedGroup, dnToEmail map[string]string, store Store, logf func(string, ...any)) (synced map[string]bool, created, updated int) {
	synced = make(map[string]bool, len(groups))
	for _, g := range groups {
		owner := dnToEmail[strings.ToLower(g.OwnerDN)] // "" if none/unresolved
		members := resolveMembers(g, dnToEmail, logf)
		isNew, err := store.UpsertLDAPGroup(g.Mail, []byte(strings.ToLower(g.Mail)), owner, members)
		if err != nil {
			logf("skip group %s: %v", g.Mail, err)
			continue
		}
		synced[strings.ToLower(g.Mail)] = true
		if isNew {
			created++
		} else {
			updated++
		}
	}
	return synced, created, updated
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

// syncContacts applies the directory's mail contacts as LDAP-mastered org contacts,
// returning the set of addresses it synced and the created/updated counts. An address the
// local directory already holds as something else (a mailbox user, a list) is logged and
// skipped rather than converted.
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

// pruneContacts deletes the mastered contacts no longer present in the directory. A
// contact with no LDAP id was made by hand and is never pruned.
func pruneContacts(synced map[string]bool, store Store) int {
	contacts, err := store.ListContacts()
	if err != nil {
		return 0
	}
	pruned := 0
	for _, c := range contacts {
		if c.LDAPID == "" || synced[strings.ToLower(c.Address)] {
			continue
		}
		if _, err := store.DeleteLDAPContact(c.Address); err == nil {
			pruned++
		}
	}
	return pruned
}

// pruneMastered deletes the mastered lists no longer present in the directory.
func pruneMastered(synced map[string]bool, store Store) int {
	lists, err := store.ListMLists()
	if err != nil {
		return 0
	}
	pruned := 0
	for _, l := range lists {
		if !l.LDAPMastered || synced[strings.ToLower(l.Listname)] {
			continue
		}
		if _, err := store.DeleteMList(l.Listname); err == nil {
			pruned++
		}
	}
	return pruned
}
