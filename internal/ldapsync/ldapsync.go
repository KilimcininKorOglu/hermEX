// Package ldapsync runs a full LDAP/AD downsync, shared by the admin CLI
// (cmd/admin ldap-sync) and the admin panel's task worker so both apply the same
// profile, photo, and group settings. It orchestrates over interfaces;
// *ldapauth.Verifier and *directory.SQLDirectory satisfy them.
package ldapsync

import (
	"fmt"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/ldapauth"
	"hermex/internal/objectstore"
)

// Syncer reads accounts and groups from the directory.
type Syncer interface {
	Sync(directory.LDAPConfig) ([]ldapauth.SyncedUser, error)
	SyncGroups(directory.LDAPConfig) ([]ldapauth.SyncedGroup, error)
}

// Store applies the downsync to the local directory.
type Store interface {
	UpsertLDAPUser(username string, externid []byte, maildir string) (bool, error)
	ApplyLDAPProfile(username string, values map[string]string) (bool, error)
	UpsertLDAPGroup(listname string, externid []byte, owner string, members []string) (bool, error)
	ListMLists() ([]directory.MListInfo, error)
	DeleteMList(listname string) (bool, error)
}

// Run performs a full downsync: each account's existence, optional profile fields
// and portrait, and (when enabled) the directory's mail-bearing groups into
// LDAP-mastered distribution lists. maildirFor maps a login to its mailbox path;
// logf records non-fatal per-entry problems (a skipped account, an unresolved
// member). It returns a one-line summary.
func Run(cfg directory.LDAPConfig, syncer Syncer, store Store, maildirFor func(string) string, logf func(string, ...any)) (string, error) {
	users, err := syncer.Sync(cfg)
	if err != nil {
		return "", err
	}
	dnToEmail := make(map[string]string, len(users))
	var created, updated int
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
		// Profile string fields into the directory; the portrait into the mailbox
		// store (after the upsert, so the maildir exists). Either failing is logged,
		// not fatal: the account itself is already synced.
		if len(u.Fields) > 0 {
			if _, err := store.ApplyLDAPProfile(u.Username, u.Fields); err != nil {
				logf("%s profile: %v", u.Username, err)
			}
		}
		if len(u.Photo) > 0 && maildir != "" {
			if st, err := objectstore.Open(maildir); err != nil {
				logf("%s photo: %v", u.Username, err)
			} else {
				if err := st.SetUserPhoto(u.Photo); err != nil {
					logf("%s photo: %v", u.Username, err)
				}
				st.Close()
			}
		}
	}
	summary := fmt.Sprintf("Synced %d directory entries: %d created, %d updated.", len(users), created, updated)

	if !cfg.SyncGroups {
		return summary, nil
	}
	groups, err := syncer.SyncGroups(cfg)
	if err != nil {
		return summary, err
	}
	synced := make(map[string]bool, len(groups))
	var gc, gu int
	for _, g := range groups {
		owner := dnToEmail[strings.ToLower(g.OwnerDN)] // "" if none/unresolved
		members := make([]string, 0, len(g.MemberDNs))
		for _, mdn := range g.MemberDNs {
			if email := dnToEmail[strings.ToLower(mdn)]; email != "" {
				members = append(members, email)
			} else {
				logf("group %s: member %q is not a synced user, skipped", g.Mail, mdn)
			}
		}
		isNew, err := store.UpsertLDAPGroup(g.Mail, []byte(strings.ToLower(g.Mail)), owner, members)
		if err != nil {
			logf("skip group %s: %v", g.Mail, err)
			continue
		}
		synced[strings.ToLower(g.Mail)] = true
		if isNew {
			gc++
		} else {
			gu++
		}
	}
	// Prune mastered lists no longer present in the directory.
	var pruned int
	if lists, err := store.ListMLists(); err == nil {
		for _, l := range lists {
			if l.LDAPMastered && !synced[strings.ToLower(l.Listname)] {
				if _, err := store.DeleteMList(l.Listname); err == nil {
					pruned++
				}
			}
		}
	}
	return summary + fmt.Sprintf(" Groups: %d created, %d updated, %d pruned (of %d).", gc, gu, pruned, len(groups)), nil
}
