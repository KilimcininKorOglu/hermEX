package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Admin role tiers for the admin API. A system admin is unrestricted; an org or
// domain admin is bound to the organization or domain named by an AdminRole's
// scope.
const (
	AdminSystem = "system"
	AdminOrg    = "org"
	AdminDomain = "domain"
)

// AdminRole is one administrative grant: the tier and the org or domain it is
// scoped to (ScopeID is 0 for a system admin).
type AdminRole struct {
	Role    string
	ScopeID int64
}

// UserID resolves a login (by its primary username) to its user id, reporting
// ok=false for an unknown login. It backs admin-role administration, which keys
// on the user id rather than the address.
func (d *SQLDirectory) UserID(login string) (id int64, ok bool, err error) {
	err = d.db.QueryRow(`SELECT id FROM users WHERE username = ?`,
		strings.ToLower(strings.TrimSpace(login))).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// GrantAdminRole grants a user an administrative role at the given scope (an
// org id for an org admin, a domain id for a domain admin, 0 for a system
// admin). Granting a role the user already holds is a no-op.
func (d *SQLDirectory) GrantAdminRole(userID int64, role string, scopeID int64) error {
	switch role {
	case AdminSystem, AdminOrg, AdminDomain:
	default:
		return fmt.Errorf("directory: unknown admin role %q", role)
	}
	_, err := d.db.Exec(
		`INSERT IGNORE INTO admin_roles (user_id, role, scope_id) VALUES (?, ?, ?)`,
		userID, role, scopeID)
	return err
}

// AdminRoles returns every administrative role a user holds (empty for a
// non-admin).
func (d *SQLDirectory) AdminRoles(userID int64) ([]AdminRole, error) {
	rows, err := d.db.Query(`SELECT role, scope_id FROM admin_roles WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminRole
	for rows.Next() {
		var r AdminRole
		if err := rows.Scan(&r.Role, &r.ScopeID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
