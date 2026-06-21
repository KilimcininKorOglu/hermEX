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
// foreign key to a user — aliases, forwards, fetchmail entries — are removed
// explicitly first, while the users still exist to be matched. Role permissions
// scoped to this domain (DomainAdmin/DomainAdminRO with the domain's id) are
// removed too; an emptied role is left in place (harmless, admin-deletable) — a
// deliberate, safe deviation from deleting it.
//
// File deletion is best-effort and happens only after the database transaction
// commits, so a storage error never leaves the directory half-purged.
func (d *SQLDirectory) PurgeDomain(domainID int64, deleteFiles bool) (bool, error) {
	var homedir string
	err := d.db.QueryRow(`SELECT homedir FROM domains WHERE id = ?`, domainID).Scan(&homedir)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var maildirs []string
	if deleteFiles {
		rows, err := d.db.Query(`SELECT maildir FROM users WHERE domain_id = ? AND maildir <> ''`, domainID)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var m string
			if err := rows.Scan(&m); err != nil {
				rows.Close()
				return false, err
			}
			maildirs = append(maildirs, m)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, err
		}
		rows.Close()
	}

	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	const usersOfDomain = `SELECT username FROM users WHERE domain_id = ?`
	if _, err := tx.Exec(`DELETE FROM aliases WHERE mainname IN (`+usersOfDomain+`)`, domainID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM forwards WHERE username IN (`+usersOfDomain+`)`, domainID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM fetchmail WHERE mailbox IN (`+usersOfDomain+`)`, domainID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM mlists WHERE domain_id = ?`, domainID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(
		`DELETE FROM role_permissions WHERE permission IN (?, ?) AND params = ?`,
		PermDomainAdmin, PermDomainAdminRO, strconv.FormatInt(domainID, 10)); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM create_defaults WHERE scope_id = ?`, domainID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM domains WHERE id = ?`, domainID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}

	if deleteFiles {
		for _, m := range maildirs {
			_ = os.RemoveAll(m)
		}
		if homedir != "" {
			_ = os.RemoveAll(homedir)
		}
	}
	return true, nil
}
