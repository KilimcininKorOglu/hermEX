// Package directory resolves recipient addresses to mailboxes and authenticates
// users. It is the account model the mail daemons consult; richer org/domain
// management and hashed credential storage arrive with the directory-database
// slice.
package directory

import (
	"crypto/subtle"
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
