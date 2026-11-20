package directory

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// Impersonator answers whether one account may act as another over EWS
// ExchangeImpersonation. It is an optional directory capability: a directory
// without it grants no impersonation.
type Impersonator interface {
	MayImpersonate(caller, target string) (bool, error)
}

// MayImpersonate reports whether caller holds the ApplicationImpersonation
// permission for target's domain, through a role a system administrator
// assigned. A mailbox's delegate list does not count: in Exchange it names
// send-on-behalf delegates, who reach the owner's folders only within the folder
// permissions, while impersonation acts as the owner with every right.
func (d *SQLDirectory) MayImpersonate(caller, target string) (bool, error) {
	uid, found, err := d.UserID(caller)
	if err != nil || !found {
		return false, err
	}
	var domainID int64
	err = d.db.QueryRow(`SELECT domain_id FROM users WHERE username = ?`,
		strings.ToLower(strings.TrimSpace(target))).Scan(&domainID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	perms, err := d.EffectivePermissions(uid)
	if err != nil {
		return false, err
	}
	scope := strconv.FormatInt(domainID, 10)
	for _, p := range perms {
		if p.Name == PermImpersonation && (p.Params == "*" || p.Params == scope) {
			return true, nil
		}
	}
	return false, nil
}
