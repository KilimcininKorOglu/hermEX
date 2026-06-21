package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Permission names for named admin roles. Each is a capability the admin API
// enforces. SystemAdmin/SystemAdminRO grant authority over everything (RO =
// read only); OrgAdmin and DomainAdmin (with a read-only DomainAdminRO variant)
// are scoped by Params to one org or domain (or "*" for all); DomainPurge and
// ResetPasswd are unscoped capabilities layered on top of an admin tier.
const (
	PermSystemAdmin   = "SystemAdmin"
	PermSystemAdminRO = "SystemAdminRO"
	PermOrgAdmin      = "OrgAdmin"
	PermDomainAdmin   = "DomainAdmin"
	PermDomainAdminRO = "DomainAdminRO"
	PermDomainPurge   = "DomainPurge"
	PermResetPasswd   = "ResetPasswd"
)

// roleNameMaxLen bounds a role name (matches the roles.name column width).
const roleNameMaxLen = 64

// Permission is one capability in a role: the capability name and its scope
// parameter. Params is empty for an unscoped capability (SystemAdmin,
// SystemAdminRO, DomainPurge, ResetPasswd), "*" for "all", or the decimal id of
// the one org (OrgAdmin) or domain (DomainAdmin, DomainAdminRO) it binds to.
type Permission struct {
	Name   string
	Params string
}

// RoleInfo is a named role's summary for listings: its identity plus the counts
// of assigned users and granted permissions.
type RoleInfo struct {
	ID          int64
	Name        string
	Description string
	UserCount   int
	PermCount   int
}

// RoleDetail is a named role with its full permission set and assigned users.
type RoleDetail struct {
	RoleInfo
	Permissions []Permission
	UserIDs     []int64
}

// permScoped reports whether a permission name carries a scope parameter and
// whether the name is known at all.
func permScoped(name string) (scoped, known bool) {
	switch name {
	case PermSystemAdmin, PermSystemAdminRO, PermDomainPurge, PermResetPasswd:
		return false, true
	case PermOrgAdmin, PermDomainAdmin, PermDomainAdminRO:
		return true, true
	}
	return false, false
}

// validatePermission rejects an unknown permission name or a Params value that
// does not match the name's scoping rule: scoped names need "*" or a decimal id,
// unscoped names must leave Params empty.
func validatePermission(p Permission) error {
	scoped, known := permScoped(p.Name)
	if !known {
		return fmt.Errorf("directory: unknown permission %q", p.Name)
	}
	if scoped {
		if p.Params == "" {
			return fmt.Errorf("directory: permission %q needs a scope", p.Name)
		}
		if p.Params != "*" {
			if _, err := strconv.ParseInt(p.Params, 10, 64); err != nil {
				return fmt.Errorf("directory: permission %q scope %q is not an id or *", p.Name, p.Params)
			}
		}
	} else if p.Params != "" {
		return fmt.Errorf("directory: permission %q takes no scope", p.Name)
	}
	return nil
}

// validRoleName trims and validates a role name (1..roleNameMaxLen characters),
// returning the trimmed value.
func validRoleName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > roleNameMaxLen {
		return "", fmt.Errorf("directory: role name must be 1..%d characters", roleNameMaxLen)
	}
	return name, nil
}

// ListRoles returns every named role, ordered by name, each with its current
// user and permission counts.
func (d *SQLDirectory) ListRoles() ([]RoleInfo, error) {
	rows, err := d.db.Query(
		`SELECT r.id, r.name, r.description,
		        (SELECT COUNT(*) FROM user_roles ur WHERE ur.role_id = r.id),
		        (SELECT COUNT(*) FROM role_permissions rp WHERE rp.role_id = r.id)
		   FROM roles r
		  ORDER BY r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoleInfo
	for rows.Next() {
		var r RoleInfo
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.UserCount, &r.PermCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRole returns one role with its full permission set and assigned user ids,
// reporting ok=false for an unknown id.
func (d *SQLDirectory) GetRole(id int64) (RoleDetail, bool, error) {
	var rd RoleDetail
	err := d.db.QueryRow(`SELECT id, name, description FROM roles WHERE id = ?`, id).
		Scan(&rd.ID, &rd.Name, &rd.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleDetail{}, false, nil
	}
	if err != nil {
		return RoleDetail{}, false, err
	}
	prows, err := d.db.Query(
		`SELECT permission, params FROM role_permissions WHERE role_id = ? ORDER BY permission, params`, id)
	if err != nil {
		return RoleDetail{}, false, err
	}
	for prows.Next() {
		var p Permission
		if err := prows.Scan(&p.Name, &p.Params); err != nil {
			prows.Close()
			return RoleDetail{}, false, err
		}
		rd.Permissions = append(rd.Permissions, p)
	}
	if err := prows.Err(); err != nil {
		prows.Close()
		return RoleDetail{}, false, err
	}
	prows.Close()
	urows, err := d.db.Query(`SELECT user_id FROM user_roles WHERE role_id = ? ORDER BY user_id`, id)
	if err != nil {
		return RoleDetail{}, false, err
	}
	defer urows.Close()
	for urows.Next() {
		var uid int64
		if err := urows.Scan(&uid); err != nil {
			return RoleDetail{}, false, err
		}
		rd.UserIDs = append(rd.UserIDs, uid)
	}
	if err := urows.Err(); err != nil {
		return RoleDetail{}, false, err
	}
	rd.PermCount = len(rd.Permissions)
	rd.UserCount = len(rd.UserIDs)
	return rd, true, nil
}

// CreateRole inserts a role with its permission set and user assignments in one
// transaction, returning the new id. The name is required, at most
// roleNameMaxLen characters, and unique; every permission must validate.
func (d *SQLDirectory) CreateRole(name, description string, perms []Permission, userIDs []int64) (int64, error) {
	name, err := validRoleName(name)
	if err != nil {
		return 0, err
	}
	for _, p := range perms {
		if err := validatePermission(p); err != nil {
			return 0, err
		}
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op
	res, err := tx.Exec(`INSERT INTO roles (name, description) VALUES (?, ?)`, name, description)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := insertRolePerms(tx, id, perms); err != nil {
		return 0, err
	}
	if err := insertRoleUsers(tx, id, userIDs); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateRole replaces a role's name, description, permission set, and user
// assignments in one transaction, reporting ok=false for an unknown id.
func (d *SQLDirectory) UpdateRole(id int64, name, description string, perms []Permission, userIDs []int64) (bool, error) {
	name, err := validRoleName(name)
	if err != nil {
		return false, err
	}
	for _, p := range perms {
		if err := validatePermission(p); err != nil {
			return false, err
		}
	}
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.Exec(`UPDATE roles SET name = ?, description = ? WHERE id = ?`, name, description, id); err != nil {
		return false, err
	}
	// A 0-row UPDATE is ambiguous (unchanged values vs missing row); confirm
	// existence explicitly so an unknown id reports ok=false.
	var one int
	if err := tx.QueryRow(`SELECT 1 FROM roles WHERE id = ?`, id).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM role_permissions WHERE role_id = ?`, id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM user_roles WHERE role_id = ?`, id); err != nil {
		return false, err
	}
	if err := insertRolePerms(tx, id, perms); err != nil {
		return false, err
	}
	if err := insertRoleUsers(tx, id, userIDs); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// DeleteRole removes a role; its permission set and user assignments cascade via
// their foreign keys. It reports ok=false for an unknown id.
func (d *SQLDirectory) DeleteRole(id int64) (bool, error) {
	res, err := d.db.Exec(`DELETE FROM roles WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// insertRolePerms writes a role's permission set, ignoring exact duplicates.
func insertRolePerms(tx *sql.Tx, roleID int64, perms []Permission) error {
	for _, p := range perms {
		if _, err := tx.Exec(
			`INSERT IGNORE INTO role_permissions (role_id, permission, params) VALUES (?, ?, ?)`,
			roleID, p.Name, p.Params); err != nil {
			return err
		}
	}
	return nil
}

// insertRoleUsers assigns a role to users, ignoring duplicate assignments.
func insertRoleUsers(tx *sql.Tx, roleID int64, userIDs []int64) error {
	for _, uid := range userIDs {
		if _, err := tx.Exec(
			`INSERT IGNORE INTO user_roles (user_id, role_id) VALUES (?, ?)`,
			uid, roleID); err != nil {
			return err
		}
	}
	return nil
}

// EffectivePermissions resolves every permission a user holds. It is the single
// authorization-resolution path: the union of the permissions of every named
// role assigned to the user and the equivalents of the user's direct admin_roles
// grants (system → SystemAdmin, org → OrgAdmin(scope), domain → DomainAdmin(scope)).
// The legacy bridge guarantees an existing admin keeps authority when the role
// model goes live — without it, the only system admin could be locked out.
func (d *SQLDirectory) EffectivePermissions(userID int64) ([]Permission, error) {
	seen := map[Permission]bool{}
	var out []Permission
	add := func(p Permission) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	rows, err := d.db.Query(
		`SELECT rp.permission, rp.params
		   FROM role_permissions rp
		   JOIN user_roles ur ON ur.role_id = rp.role_id
		  WHERE ur.user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.Name, &p.Params); err != nil {
			rows.Close()
			return nil, err
		}
		add(p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	legacy, err := d.AdminRoles(userID)
	if err != nil {
		return nil, err
	}
	for _, r := range legacy {
		switch r.Role {
		case AdminSystem:
			add(Permission{Name: PermSystemAdmin})
		case AdminOrg:
			add(Permission{Name: PermOrgAdmin, Params: strconv.FormatInt(r.ScopeID, 10)})
		case AdminDomain:
			add(Permission{Name: PermDomainAdmin, Params: strconv.FormatInt(r.ScopeID, 10)})
		}
	}
	return out, nil
}
