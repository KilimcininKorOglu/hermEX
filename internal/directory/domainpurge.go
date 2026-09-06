package directory

import (
	"database/sql"
	"errors"
	"os"
	"strconv"
)

// PurgeDomain hard-removes a domain and everything scoped to it in one
// transaction, then (when deleteFiles is set) removes the on-disk mailboxes and
// the domain directory. It reports ok=false for an unknown id.
//
// The cascade mirrors the per-user delete done domain-wide. Foreign keys carry
// most of it: deleting the domain row cascades its users, and each user cascades
// its altnames, admin-role grants, named-role assignments, and properties; a
// distribution list's pseudo-user cascades its membership rows. The rows with no
// foreign key to a user (aliases, forwards, fetchmail entries) are removed
// explicitly first, while the users still exist to be matched; aliases are cleared
// from both ends, since one addressed INTO this domain belongs to a user elsewhere
// and no user-keyed delete would reach it. Role permissions
// scoped to this domain (DomainAdmin/DomainAdminRO with the domain's id) are
// removed too; an emptied role is left in place (harmless, admin-deletable), a
// deliberate, safe deviation from deleting it.
//
// File deletion is best-effort and happens only after the database transaction
// commits, so a storage error never leaves the directory half-purged.
func (d *SQLDirectory) PurgeDomain(domainID int64, deleteFiles bool) (bool, error) {
	var homedir, domainname string
	err := d.db.QueryRow(`SELECT homedir, domainname FROM domains WHERE id = ?`, domainID).Scan(&homedir, &domainname)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var maildirs []string
	if deleteFiles {
		if maildirs, err = d.domainMaildirs(domainID); err != nil {
			return false, err
		}
	}

	if err := d.purgeDomainTx(domainID, domainname); err != nil {
		return false, err
	}
	if deleteFiles {
		removeDomainFiles(maildirs, homedir)
	}
	return true, nil
}

// purgeDomainTx runs the whole cascade in one transaction, so a failure leaves
// the directory as it was.
func (d *SQLDirectory) purgeDomainTx(domainID int64, domainname string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op
	if err := purgeDomainRows(tx, domainID, domainname); err != nil {
		return err
	}
	return tx.Commit()
}

// removeDomainFiles drops the mailboxes and the domain directory. It runs only
// after the transaction commits, and is best-effort: a storage error never
// leaves the directory half-purged.
func removeDomainFiles(maildirs []string, homedir string) {
	for _, m := range maildirs {
		_ = os.RemoveAll(m)
	}
	if homedir != "" {
		_ = os.RemoveAll(homedir)
	}
}

// domainMaildirs lists the on-disk mailbox paths of a domain's users, read
// before the cascade removes the rows that name them.
func (d *SQLDirectory) domainMaildirs(domainID int64) ([]string, error) {
	rows, err := d.db.Query(`SELECT maildir FROM users WHERE domain_id = ? AND maildir <> ''`, domainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var maildirs []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		maildirs = append(maildirs, m)
	}
	return maildirs, rows.Err()
}

// purgeDomainRows deletes, in one transaction, every row scoped to the domain
// that no foreign key would carry away with it.
func purgeDomainRows(tx *sql.Tx, domainID int64, domainname string) error {
	const usersOfDomain = `SELECT username FROM users WHERE domain_id = ?`
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM aliases WHERE mainname IN (` + usersOfDomain + `)`, []any{domainID}},
		// Also the aliases pointing INTO this domain from a user elsewhere. Those
		// are keyed by address string with no foreign key, so nothing above reaches
		// them, and each is reported by Identities as an address its owner may send
		// as: left behind, they would let an account in a surviving domain keep
		// claiming addresses in the one that was just removed.
		{`DELETE FROM aliases WHERE SUBSTRING_INDEX(aliasname, '@', -1) = ?`, []any{domainname}},
		{`DELETE FROM forwards WHERE username IN (` + usersOfDomain + `)`, []any{domainID}},
		{`DELETE FROM fetchmail WHERE mailbox IN (` + usersOfDomain + `)`, []any{domainID}},
		{`DELETE FROM mlists WHERE domain_id = ?`, []any{domainID}},
		{`DELETE FROM role_permissions WHERE permission IN (?, ?) AND params = ?`,
			[]any{PermDomainAdmin, PermDomainAdminRO, strconv.FormatInt(domainID, 10)}},
		{`DELETE FROM create_defaults WHERE scope_id = ?`, []any{domainID}},
		// Last: the domain row itself, whose foreign keys carry away the users and
		// everything keyed to them.
		{`DELETE FROM domains WHERE id = ?`, []any{domainID}},
	}
	for _, s := range statements {
		if _, err := tx.Exec(s.query, s.args...); err != nil {
			return err
		}
	}
	return nil
}
