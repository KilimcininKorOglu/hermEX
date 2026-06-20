package objectstore

import (
	"database/sql"
	"errors"
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
			err = upsertPermission(tx, folderID, addUsername(c), c.Rights)
		case PermModify:
			if username, special := specialUsername(c.MemberID); special {
				// A Modify of the default/anonymous member must create its row when
				// none is stored (the client edits the synthesized member), so it
				// upserts by username — never a no-op UPDATE that drops the edit.
				err = upsertPermission(tx, folderID, username, c.Rights)
			} else {
				_, err = tx.Exec(`UPDATE permissions SET permission=? WHERE folder_id=? AND member_id=?`,
					int64(c.Rights), folderID, c.MemberID)
			}
		case PermRemove:
			clause, arg := memberLocator(c.MemberID)
			_, err = tx.Exec(`DELETE FROM permissions WHERE folder_id=? AND `+clause, append([]any{folderID}, arg...)...)
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

// ResolvePermission computes a user's effective rights on a folder, following the
// MS-OXCPERM resolution order: an exact-username grant wins; absent that, the
// "default" member grant applies; absent that, no rights. The group/DL step the
// reference unions in (every mailing-list grant the user belongs to) is a documented
// v1 gap — hermEX models aliases/altnames, not mailing lists — and is skipped; the
// store-level configurations fallback (config_id 8/9) is unused in v1 and likewise
// yields no rights. An anonymous caller (empty username) matches the stored
// anonymous ("") row through the exact lookup. Rights come back as the stored,
// already-normalized bitfield for the caller (free/busy, delegate access) to read.
func (s *Store) ResolvePermission(folderID int64, username string) (uint32, error) {
	// A store owner has read-write access to every object in the mailbox, so it
	// resolves to full member rights on any folder regardless of that folder's own
	// ACL. Checking here keeps the elevation in the single permission resolver every
	// access goes through, rather than at each call site where one could be missed.
	if owner, err := s.IsStoreOwner(username); err != nil {
		return 0, err
	} else if owner {
		return mapi.RightsMaxROP, nil
	}
	if rights, ok, err := s.lookupPermission(folderID, username); err != nil || ok {
		return rights, err
	}
	// The DL/group union is the documented v1 gap (no mailing-list model), skipped.
	rights, _, err := s.lookupPermission(folderID, "default")
	return rights, err
}

// lookupPermission reads the rights stored for an exact username on a folder,
// reporting whether such a row exists. A missing row is not an error.
func (s *Store) lookupPermission(folderID int64, username string) (uint32, bool, error) {
	var perm int64
	err := s.objdb.QueryRow(
		`SELECT permission FROM permissions WHERE folder_id=? AND username=?`,
		folderID, username).Scan(&perm)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		s.logStoreError("resolve-permission", err)
		return 0, false, err
	}
	return uint32(perm), true, nil
}

// HasFolderGrant reports whether the user holds a non-zero permission on any folder
// under their OWN username — a caller-specific grant, distinct from the universal
// "default" member. It is the store-open primitive: a delegate logon requires this or
// a delegate designation, so the always-present default free/busy grant does not by
// itself let every authenticated user open every mailbox. The per-folder
// ResolvePermission still honours the default grant — the open gate and the
// per-folder gate use different criteria on purpose ("may you get a session at all"
// vs "what may you do once in"). A default-member or group grant alone therefore does
// not enable a ROP store-open (free/busy is served via NSPI/EWS/CalDAV, not a logon);
// the group/mailing-list case is the same documented v1 gap as ResolvePermission.
func (s *Store) HasFolderGrant(username string) (bool, error) {
	var one int
	err := s.objdb.QueryRow(
		`SELECT 1 FROM permissions WHERE username=? AND permission!=0 LIMIT 1`, username).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		s.logStoreError("has-folder-grant", err)
		return false, err
	}
	return true, nil
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

// upsertPermission inserts a member's row or updates its rights, keyed by the unique
// (folder_id, username) index — the shared write for an Add and for a Modify of a
// special member that has no stored row yet.
func upsertPermission(tx *sql.Tx, folderID int64, username string, rights uint32) error {
	_, err := tx.Exec(
		`INSERT INTO permissions (folder_id, username, permission) VALUES (?, ?, ?)
		 ON CONFLICT(folder_id, username) DO UPDATE SET permission=excluded.permission`,
		folderID, username, int64(rights))
	return err
}

// specialUsername maps a special member id to its stored username — 0 → "default",
// -1 → "" (anonymous) — reporting false for a real member (addressed by row id).
func specialUsername(memberID int64) (string, bool) {
	switch memberID {
	case mapi.MemberIDDefault:
		return "default", true
	case mapi.MemberIDAnonymous:
		return "", true
	default:
		return "", false
	}
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
	username, _ := specialUsername(c.MemberID)
	return username
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
