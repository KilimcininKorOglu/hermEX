// Package directory resolves recipient addresses to mailboxes and authenticates
// users. It is the account model the mail daemons consult; richer org/domain
// management and hashed credential storage arrive with the directory-database
// slice.
package directory

import (
	"crypto/subtle"
	"sort"
	"strings"
)

// Account is one mailbox account. Password is a placeholder plaintext secret
// for the static/test account model; the directory-database slice will replace
// it with a hashed credential.
type Account struct {
	Password    string
	MailboxPath string
}

// Accounts resolves a recipient email address to its mailbox store path. ok is
// false for unknown addresses, so delivery to them is refused rather than
// relayed.
type Accounts interface {
	Resolve(address string) (mailboxPath string, ok bool)
}

// Authenticator verifies a user's credentials and yields their mailbox store
// path. ok is false when the user is unknown or the password is wrong.
type Authenticator interface {
	Authenticate(user, password string) (mailboxPath string, ok bool)
}

// Identifier optionally enumerates the addresses a user is permitted to send
// as: their primary address plus any aliases/altnames. It backs the webmail
// From/send-as gating, which must reject any From not in this set. Directories
// that do not implement it offer send-as-self only. On-behalf/delegate sending
// is a separate permissions feature, out of scope here.
type Identifier interface {
	Identities(user string) ([]string, error)
}

// MailboxLister enumerates the store paths of every mailbox the directory knows.
// A background worker with no address to resolve — the send-later spooler, which
// must scan each user's Outbox — uses it to find all stores. Directories that
// cannot enumerate may omit it; the spooler then has nothing to scan.
type MailboxLister interface {
	Maildirs() ([]string, error)
}

// GALEntry is one Global Address List entry returned by a recipient search: a
// directory user's address and a display name for it. Until per-user display
// names are stored — they live in user_properties (the internal spec §2.10),
// resolved PR_DISPLAY_NAME → PR_NICKNAME → username, which the directory-database
// slice will add — DisplayName falls back to the address. That is the
// degenerate-correct case of the same GAL, not a placeholder.
type GALEntry struct {
	DisplayName string
	Address     string
}

// GAL optionally searches the Global Address List — the directory's mailbox
// users — for entries whose address matches a typed query, backing webmail
// recipient autocomplete and "check names" resolution. Directories that cannot
// enumerate users may omit it; webmail then offers no suggestions.
type GAL interface {
	SearchGAL(query string, limit int) ([]GALEntry, error)
}

// StaticAccounts is a fixed map of lowercase address/username to Account. It
// implements both Accounts and Authenticator and suits tests and small
// deployments.
type StaticAccounts map[string]Account

// Resolve implements Accounts.
func (a StaticAccounts) Resolve(address string) (string, bool) {
	acc, ok := a[strings.ToLower(address)]
	if !ok {
		return "", false
	}
	return acc.MailboxPath, true
}

// Identities implements Identifier: a static account may send only as itself.
func (a StaticAccounts) Identities(user string) ([]string, error) {
	if _, ok := a[strings.ToLower(user)]; !ok {
		return nil, nil
	}
	return []string{strings.ToLower(user)}, nil
}

// Maildirs implements MailboxLister: the distinct mailbox paths of all accounts
// (several addresses may share one mailbox, so duplicates are collapsed).
func (a StaticAccounts) Maildirs() ([]string, error) {
	seen := make(map[string]bool, len(a))
	out := make([]string, 0, len(a))
	for _, acc := range a {
		if acc.MailboxPath == "" || seen[acc.MailboxPath] {
			continue
		}
		seen[acc.MailboxPath] = true
		out = append(out, acc.MailboxPath)
	}
	return out, nil
}

// SearchGAL implements GAL: a case-insensitive substring match over the account
// addresses, collapsed to one entry per mailbox so aliases that share a mailbox
// do not suggest the same person twice. Results are ordered by address and
// capped at limit (limit <= 0 means no cap). DisplayName mirrors the address.
func (a StaticAccounts) SearchGAL(query string, limit int) ([]GALEntry, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	addrs := make([]string, 0, len(a))
	for addr := range a {
		if q == "" || strings.Contains(strings.ToLower(addr), q) {
			addrs = append(addrs, addr)
		}
	}
	sort.Strings(addrs)
	seen := make(map[string]bool, len(addrs))
	out := make([]GALEntry, 0, len(addrs))
	for _, addr := range addrs {
		if mbox := a[addr].MailboxPath; mbox == "" || seen[mbox] {
			continue
		} else {
			seen[mbox] = true
		}
		out = append(out, GALEntry{DisplayName: addr, Address: addr})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Authenticate implements Authenticator using a constant-time password compare.
func (a StaticAccounts) Authenticate(user, password string) (string, bool) {
	acc, ok := a[strings.ToLower(user)]
	if !ok {
		// Compare against a dummy so the work is similar for unknown users.
		subtle.ConstantTimeCompare([]byte(password), []byte(password))
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(password), []byte(acc.Password)) != 1 {
		return "", false
	}
	return acc.MailboxPath, true
}
