package objectstore

import (
	"fmt"

	"hermex/internal/mapi"
)

// PermissionEntry is one row of a folder's permission table as the wire serves it:
// the member's wire id, the member display name, and the rights bitfield. The wire
// id is the MS-OXCPERM translation of the stored username — 0 for the "default"
// member, -1 for the anonymous member (stored as ""), else the row's own id.
type PermissionEntry struct {
	MemberID int64  // PR_MEMBER_ID (0=default, -1=anonymous, else the row id)
	Name     string // PR_MEMBER_NAME ("default"/"anonymous" or the username)
	Rights   uint32 // PR_MEMBER_RIGHTS
}

// PermissionOp is a decoded RopModifyPermissions row operation. The zero value is
// invalid so a half-built change is rejected rather than silently treated as add.
type PermissionOp uint8

const (
	PermAdd    PermissionOp = iota + 1 // create or replace a member's row
	PermModify                         // change an existing member's rights
	PermRemove                         // drop a member's row
)

// PermissionChange is one decoded RopModifyPermissions row. The ROP layer fills it
// after masking the rights with mapi.RightsMaxROP and applying mapi.NormalizeRights:
// the store persists Rights verbatim and does not re-mask. MemberID addresses the
// target for Modify/Remove (and selects the special members for Add); Username is the
// resolved storage name for an Add of a real member.
type PermissionChange struct {
	Op       PermissionOp
	MemberID int64  // 0=default, -1=anonymous, else the target row id
	Username string // storage username for an Add of a real member
	Rights   uint32 // already masked + normalized by the caller
}

// ListPermissions returns a folder's stored permission rows, translating each stored
// username to its wire member id and display name (the "default" row → id 0, the ""
// row → id -1/"anonymous", a real member → its row id/username). Rows come back in
// member-id order. The special-member rows are returned only when actually stored;
// synthesizing the always-present default/anonymous entries a client expects is the
// caller's (table layer's) job, since it is wire presentation, not storage.
func (s *Store) ListPermissions(folderID int64) ([]PermissionEntry, error) {
	rows, err := s.objdb.Query(
		`SELECT member_id, username, permission FROM permissions WHERE folder_id=? ORDER BY member_id`,
		folderID)
	if err != nil {
		s.logStoreError("list-permissions", err)
		return nil, err
	}
	defer rows.Close()

	var out []PermissionEntry
	for rows.Next() {
		var (
			rowID    int64
			username string
			perm     int64
		)
		if err := rows.Scan(&rowID, &username, &perm); err != nil {
			s.logStoreError("list-permissions", err)
			return nil, err
		}
		out = append(out, PermissionEntry{
			MemberID: wireMemberID(rowID, username),
			Name:     memberDisplayName(username),
			Rights:   uint32(perm),
		})
	}
	if err := rows.Err(); err != nil {
		s.logStoreError("list-permissions", err)
		return nil, err
	}
	return out, nil
}

// ModifyPermissions applies a decoded RopModifyPermissions batch to a folder. When
// replace is set the folder's whole permission set is cleared first (the
// REPLACEROWS flag). Each change is keyed by its operation: an Add upserts the
// member's row, a Modify rewrites an existing member's rights, a Remove drops it.
// The member is located by the MS-OXCPERM rule — id 0/-1 map to the stored
// "default"/"" rows, any other id to the row with that id — so a client editing the
// default free/busy permission (always id 0) addresses the seeded row, not a rowid.
// The whole batch runs in one transaction.
func (s *Store) ModifyPermissions(folderID int64, replace bool, changes []PermissionChange) error {
	tx, err := s.objdb.Begin()
	if err != nil {
		s.logStoreError("modify-permissions", err)
		return err
	}
	defer tx.Rollback()

	if replace {
		if _, err := tx.Exec(`DELETE FROM permissions WHERE folder_id=?`, folderID); err != nil {
			s.logStoreError("modify-permissions", err)
			return err
		}
	}

	for _, c := range changes {
		switch c.Op {
		case PermAdd:
			_, err = tx.Exec(
				`INSERT INTO permissions (folder_id, username, permission) VALUES (?, ?, ?)
				 ON CONFLICT(folder_id, username) DO UPDATE SET permission=excluded.permission`,
				folderID, addUsername(c), int64(c.Rights))
		case PermModify:
			clause, arg := memberLocator(c.MemberID)
			params := append([]any{int64(c.Rights), folderID}, arg...)
			_, err = tx.Exec(`UPDATE permissions SET permission=? WHERE folder_id=? AND `+clause, params...)
		case PermRemove:
			clause, arg := memberLocator(c.MemberID)
			params := append([]any{folderID}, arg...)
			_, err = tx.Exec(`DELETE FROM permissions WHERE folder_id=? AND `+clause, params...)
		default:
			return fmt.Errorf("objectstore: unknown permission op %d", c.Op)
		}
		if err != nil {
			s.logStoreError("modify-permissions", err)
			return err
		}
	}
	return tx.Commit()
}

// wireMemberID maps a stored row to its PR_MEMBER_ID: the "default" username reports
// 0, the empty (anonymous) username reports -1, every real member reports its row id.
func wireMemberID(rowID int64, username string) int64 {
	switch username {
	case "default":
		return mapi.MemberIDDefault
	case "":
		return mapi.MemberIDAnonymous
	default:
		return rowID
	}
}

// memberDisplayName maps a stored username to PR_MEMBER_NAME: the empty (anonymous)
// row presents as "anonymous"; every other row presents its username verbatim
// (including the literal "default").
func memberDisplayName(username string) string {
	if username == "" {
		return "anonymous"
	}
	return username
}

// addUsername resolves the storage username for an Add. A real-member Add carries a
// resolved Username (an SMTP address, never the literal "default"/"anonymous"), so a
// non-empty Username wins and MemberID is ignored — this is what keeps a real Add
// whose MemberID is the int64 zero value from being misrouted to the default member.
// A special-member Add carries an empty Username and selects default vs anonymous by
// its MemberID (the wire 0 / -1).
func addUsername(c PermissionChange) string {
	if c.Username != "" {
		return c.Username
	}
	if c.MemberID == mapi.MemberIDAnonymous {
		return ""
	}
	return "default"
}

// memberLocator returns the WHERE-clause fragment and its bound arguments that select
// the row a Modify/Remove addresses: a special member by its stored username (no
// bound arg), a real member by row id. The clause is a fixed literal, never client
// data, so it is safe to concatenate into the statement.
func memberLocator(memberID int64) (clause string, args []any) {
	switch memberID {
	case mapi.MemberIDDefault:
		return "username='default'", nil
	case mapi.MemberIDAnonymous:
		return "username=''", nil
	default:
		return "member_id=?", []any{memberID}
	}
}
