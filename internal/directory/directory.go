// Package directory resolves recipient addresses to the mailboxes that hold
// them. It is the account model the mail daemons consult; richer org/domain
// management is added as later slices need it.
package directory

import "strings"

// Accounts resolves a recipient email address to the filesystem path of its
// mailbox store. ok is false for unknown addresses, so delivery to them is
// refused rather than relayed.
type Accounts interface {
	Resolve(address string) (mailboxPath string, ok bool)
}

// StaticAccounts is a fixed address→mailbox-path map. Lookups are
// case-insensitive; callers should store keys in lower case.
type StaticAccounts map[string]string

// Resolve implements Accounts.
func (a StaticAccounts) Resolve(address string) (string, bool) {
	path, ok := a[strings.ToLower(address)]
	return path, ok
}
