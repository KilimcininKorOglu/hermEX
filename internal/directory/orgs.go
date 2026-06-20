package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// orgNameMaxLen bounds an organization name. The reference admin UI requires
// fewer than 33 characters; this is that limit.
const orgNameMaxLen = 32

// OrgInfo is an organization's administrative summary: its id, display name,
// description, and the number of domains currently assigned to it.
type OrgInfo struct {
	ID          int64
	Name        string
	Description string
	DomainCount int
}

// ListOrgs returns every organization, ordered by name, each with its current
// domain count.
func (d *SQLDirectory) ListOrgs() ([]OrgInfo, error) {
	rows, err := d.db.Query(
		`SELECT o.id, o.name, o.description, COUNT(dm.id)
		   FROM orgs o LEFT JOIN domains dm ON dm.org_id = o.id
		  GROUP BY o.id, o.name, o.description
		  ORDER BY o.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgInfo
	for rows.Next() {
		var o OrgInfo
		if err := rows.Scan(&o.ID, &o.Name, &o.Description, &o.DomainCount); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// GetOrg returns one organization with its domain count, reporting ok=false for
// an unknown id.
func (d *SQLDirectory) GetOrg(id int64) (OrgInfo, bool, error) {
	var o OrgInfo
	err := d.db.QueryRow(
		`SELECT o.id, o.name, o.description, COUNT(dm.id)
		   FROM orgs o LEFT JOIN domains dm ON dm.org_id = o.id
		  WHERE o.id = ?
		  GROUP BY o.id, o.name, o.description`, id).
		Scan(&o.ID, &o.Name, &o.Description, &o.DomainCount)
	if errors.Is(err, sql.ErrNoRows) {
		return OrgInfo{}, false, nil
	}
	if err != nil {
		return OrgInfo{}, false, err
	}
	return o, true, nil
}

// validOrgName trims and validates an organization name (1..orgNameMaxLen
// characters), returning the trimmed value.
func validOrgName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > orgNameMaxLen {
		return "", fmt.Errorf("directory: organization name must be 1..%d characters", orgNameMaxLen)
	}
	return name, nil
}

// CreateOrg inserts an organization and returns its id. The name is required,
// at most orgNameMaxLen characters, and unique; domains are attached separately
// with AssignDomainToOrg.
func (d *SQLDirectory) CreateOrg(name, description string) (int64, error) {
	name, err := validOrgName(name)
	if err != nil {
		return 0, err
	}
	res, err := d.db.Exec(`INSERT INTO orgs (name, description) VALUES (?, ?)`, name, description)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateOrg replaces an organization's name and description, reporting ok=false
// for an unknown id.
func (d *SQLDirectory) UpdateOrg(id int64, name, description string) (bool, error) {
	name, err := validOrgName(name)
	if err != nil {
		return false, err
	}
	res, err := d.db.Exec(`UPDATE orgs SET name = ?, description = ? WHERE id = ?`, name, description, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteOrg deletes an organization and detaches its dependents in one
// transaction: its domains become organizationless (org_id 0) and its
// org-scoped configuration — the LDAP config, the default sync policy, and any
// org-admin grants — is removed. It reports ok=false for an unknown id and
// refuses id 0: that is the reserved "organizationless" sentinel, and a delete
// scoped to it would wipe shared rows such as the global default sync policy.
func (d *SQLDirectory) DeleteOrg(id int64) (bool, error) {
	if id == 0 {
		return false, errors.New("directory: cannot delete the reserved organizationless id 0")
	}
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op
	res, err := tx.Exec(`DELETE FROM orgs WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE domains SET org_id = 0 WHERE org_id = ?`, id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM ldap_config WHERE org_id = ?`, id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM sync_policy WHERE org_id = ?`, id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM admin_roles WHERE role = ? AND scope_id = ?`, AdminOrg, id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// AssignDomainToOrg attaches a domain to an organization, or detaches it when
// orgID is 0. A nonzero orgID must name an existing organization, so a domain is
// never orphaned onto a missing org. It reports ok=false for an unknown domain.
func (d *SQLDirectory) AssignDomainToOrg(domainID, orgID int64) (bool, error) {
	if orgID != 0 {
		var one int
		err := d.db.QueryRow(`SELECT 1 FROM orgs WHERE id = ?`, orgID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("directory: organization %d not found", orgID)
		}
		if err != nil {
			return false, err
		}
	}
	res, err := d.db.Exec(`UPDATE domains SET org_id = ? WHERE id = ?`, orgID, domainID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
