package objectstore

import (
	"database/sql"
	"errors"
	"slices"
	"time"

	"hermex/internal/mapi"
)

// FolderInfo is the per-folder metadata needed to enumerate a mailbox's folder
// tree (e.g. for IMAP LIST). ParentID is nil for a top-level folder, one
// directly under the IPM subtree, which clients see as a mailbox root.
type FolderInfo struct {
	ID          int64
	ParentID    *int64
	DisplayName string
	Subscribed  bool
}

// folderNode is an (id, parent) pair from a hierarchy walk.
type folderNode struct {
	id     int64
	parent int64
}

// CreateFolder creates a folder under parent and returns its id. A nil parent
// places it at the top level, directly under the IPM subtree, which is where
// clients create their own folders. The folder is provisioned like a built-in
// one: a freshly allocated id, a message-id range, a change number, and the
// standard property bag (display name, note container class, timestamps,
// change key). Callers guard against duplicate names via FolderByName;
// built-in folders are addressed by their fixed ids and never created here.
func (s *Store) CreateFolder(parent *int64, displayName string) (int64, error) {
	replica, err := s.replicaGUID()
	if err != nil {
		return 0, err
	}
	parentFID := s.ipmSubtree()
	if parent != nil {
		parentFID = *parent
	}
	tx, err := s.objdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	fid, cn, err := insertFolderRow(tx, parentFID)
	if err != nil {
		return 0, err
	}
	props, err := folderPropertyBag(tx, replica, mapi.UnixToNTTime(time.Now()), cn,
		displayName, mapi.ContainerClassNote, true, false)
	if err != nil {
		return 0, err
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	if err := insertProps(tx, "folder_properties", "folder_id", int64(fid), props); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.publishChange("folder", cn, "")
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	return int64(fid), nil
}

// insertFolderRow allocates a new folder's id, its reserved message-id range and
// its change number, and writes the row. The range is carved at creation so
// every message the folder later holds draws its id from the folder's own block.
func insertFolderRow(tx *sql.Tx, parentFID int64) (fid, cn uint64, err error) {
	fid, err = allocateEID(tx)
	if err != nil {
		return 0, 0, err
	}
	begin, end, err := allocateRange(tx)
	if err != nil {
		return 0, 0, err
	}
	cn, err = allocateCN(tx)
	if err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(
		`INSERT INTO folders (folder_id, parent_id, change_number, cur_eid, max_eid) VALUES (?, ?, ?, ?, ?)`,
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		int64(fid), parentFID, int64(cn), int64(begin), int64(end)); err != nil {
		return 0, 0, err
	}
	return fid, cn, nil
}

// FolderByName looks up a folder by parent and display name, reporting
// ok=false when none matches. A nil parent searches the top level (the IPM
// subtree's direct children). The name is matched against PR_DISPLAY_NAME.
func (s *Store) FolderByName(parent *int64, name string) (id int64, ok bool, err error) {
	parentFID := s.ipmSubtree()
	if parent != nil {
		parentFID = *parent
	}
	rows, err := s.objdb.Query(
		`SELECT folder_id FROM folders WHERE parent_id=? AND is_deleted=0`, parentFID)
	if err != nil {
		return 0, false, err
	}
	var ids []int64
	for rows.Next() {
		var fid int64
		if err := rows.Scan(&fid); err != nil {
			_ = rows.Close()
			return 0, false, err
		}
		ids = append(ids, fid)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	for _, fid := range ids {
		dn, err := s.folderDisplayName(fid)
		if err != nil {
			return 0, false, err
		}
		if dn == name {
			return fid, true, nil
		}
	}
	return 0, false, nil
}

// ListFolders returns the client-visible folder tree: every non-hidden,
// non-search folder in the IPM subtree, ordered by id. The subtree's direct
// children are reported with a nil ParentID (clients treat them as roots);
// deeper folders keep their real parent. The root container and its system
// folders are not included.
func (s *Store) ListFolders() ([]FolderInfo, error) {
	nodes, err := s.descendants(s.ipmSubtree())
	if err != nil {
		return nil, err
	}
	var out []FolderInfo
	for _, n := range nodes {
		props, err := s.GetFolderProperties(n.id, mapi.PrDisplayName, mapi.PrAttrHidden)
		if err != nil {
			return nil, err
		}
		if boolProp(props, mapi.PrAttrHidden) {
			continue
		}
		sub, err := s.folderSubscribed(n.id)
		if err != nil {
			return nil, err
		}
		dn, _ := stringProp(props, mapi.PrDisplayName)
		fi := FolderInfo{ID: n.id, DisplayName: dn, Subscribed: sub}
		if n.parent != s.ipmSubtree() {
			p := n.parent
			fi.ParentID = &p
		}
		out = append(out, fi)
	}
	return out, nil
}

// RenameFolder moves a folder under newParent (nil for the top level) and sets
// its display name. It reports ErrNotFound when the folder is missing.
func (s *Store) RenameFolder(folderID int64, newParent *int64, newName string) error {
	// Renaming or reparenting a folder the store provisions itself would leave
	// every surface that addresses it by PrivateFID_* constant pointing at a
	// folder the user no longer recognises, so no protocol may.
	if s.isBuiltinFolder(folderID) {
		return ErrBuiltinFolder
	}
	parentFID := s.ipmSubtree()
	if newParent != nil {
		parentFID = *newParent
		// Reparenting a folder into itself or one of its own descendants would
		// make the folder its own ancestor (an unwalkable cycle); refuse it, as
		// CopyFolder does. folderSubtreeIDs includes the folder itself, so this
		// catches both the self-move and the descendant-move.
		subtree, err := s.folderSubtreeIDs(folderID)
		if err != nil {
			return err
		}
		if slices.Contains(subtree, parentFID) {
			return ErrFolderCycle
		}
	}
	res, err := s.objdb.Exec(
		`UPDATE folders SET parent_id=? WHERE folder_id=? AND is_deleted=0`, parentFID, folderID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := s.SetFolderProperties(folderID,
		mapi.PropertyValues{{Tag: mapi.PrDisplayName, Value: newName}}); err != nil {
		return err
	}
	// Keep the index projection's name in step where a row exists.
	_, err = s.idxdb.Exec(`UPDATE folders SET name=? WHERE folder_id=?`, newName, folderID)
	return err
}

// SetFolderName renames a folder in place: it updates the display name and the
// index projection's name (keeping the IMAP listing in step) without changing the
// folder's parent, the half of RenameFolder a pure rename needs. It refuses a
// name already held by a live sibling (ErrFolderExists) so name-based resolution
// stays unambiguous, and reports ErrNotFound when the folder is missing.
func (s *Store) SetFolderName(folderID int64, newName string) error {
	var parentFID int64
	err := s.objdb.QueryRow(
		`SELECT parent_id FROM folders WHERE folder_id=? AND is_deleted=0`, folderID).Scan(&parentFID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var parentArg *int64
	if parentFID != s.ipmSubtree() {
		parentArg = &parentFID
	}
	if existing, ok, err := s.FolderByName(parentArg, newName); err != nil {
		return err
	} else if ok && existing != folderID {
		return ErrFolderExists
	}
	if err := s.SetFolderProperties(folderID,
		mapi.PropertyValues{{Tag: mapi.PrDisplayName, Value: newName}}); err != nil {
		return err
	}
	_, err = s.idxdb.Exec(`UPDATE folders SET name=? WHERE folder_id=?`, newName, folderID)
	return err
}

// DeleteFolder removes a folder and its descendants: the object subtree (a
// foreign-key cascade drops child folders, messages, and property bags) and
// the matching index rows, mappings, and cached eml files. It reports
// ErrNotFound when no such folder exists.
//
// A built-in folder is refused with ErrBuiltinFolder. The store provisions the
// hierarchy every surface addresses by PrivateFID_* constant, and the cascade
// would take every message in it, so removing one is not a user operation on any
// protocol.
func (s *Store) DeleteFolder(folderID int64) error {
	if s.isBuiltinFolder(folderID) {
		return ErrBuiltinFolder
	}
	subtree, err := s.folderSubtreeIDs(folderID)
	if err != nil {
		return err
	}
	if len(subtree) == 0 {
		return ErrNotFound
	}
	// The folder rows go, and the foreign key takes every message with them, so
	// the mail has to leave first. Recoverable Items is the dumpster every other
	// hard delete routes to, and a folder delete is no different.
	if err := s.dumpsterSubtree(subtree); err != nil {
		return err
	}
	res, err := s.objdb.Exec(`DELETE FROM folders WHERE folder_id=?`, folderID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	for _, fid := range subtree {
		if err := s.dropIndexFolder(fid); err != nil {
			return err
		}
	}
	s.publishChange("folder", 0, "")
	return nil
}

// CopyFolder copies the folder srcFolderID under newParent with the display name
// newName and returns the new folder's id. The folder's messages are re-filed into
// the copy (preserving each message's flags and received date); with recursive
// set, its live subfolders are copied depth-first, each under the new folder.
// Copying a folder into its own subtree is refused with ErrFolderCycle (it would
// recurse without end). v1 carries the display name and contents; other
// folder-level properties are not yet copied.
func (s *Store) CopyFolder(srcFolderID, newParent int64, newName string, recursive bool) (int64, error) {
	exists, err := s.FolderExists(srcFolderID)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, ErrNotFound
	}
	subtree, err := s.folderSubtreeIDs(srcFolderID)
	if err != nil {
		return 0, err
	}
	if slices.Contains(subtree, newParent) {
		return 0, ErrFolderCycle
	}
	return s.copyFolderInto(srcFolderID, newParent, newName, recursive)
}

// copyFolderInto is the recursion body of CopyFolder, run after the cycle check.
func (s *Store) copyFolderInto(srcFolderID, newParent int64, newName string, recursive bool) (int64, error) {
	newID, err := s.CreateFolder(&newParent, newName)
	if err != nil {
		return 0, err
	}
	if err := s.copyFolderMessages(srcFolderID, newID); err != nil {
		return 0, err
	}
	if !recursive {
		return newID, nil
	}
	if err := s.copySubfolders(srcFolderID, newID); err != nil {
		return 0, err
	}
	return newID, nil
}

// copyFolderMessages re-files every message from one folder into another,
// preserving each message's flags and received date.
func (s *Store) copyFolderMessages(srcFolderID, dstFolderID int64) error {
	msgs, err := s.ListMessages(srcFolderID)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		raw, err := s.GetMessageRaw(srcFolderID, m.UID)
		if err != nil {
			return err
		}
		if _, err := s.AppendMessage(dstFolderID, raw, m.InternalDate, m.Flags); err != nil {
			return err
		}
	}
	return nil
}

// copySubfolders copies each direct subfolder's whole subtree under the new
// parent, keeping each one's display name.
func (s *Store) copySubfolders(srcFolderID, dstFolderID int64) error {
	children, err := s.childFolderIDs(srcFolderID)
	if err != nil {
		return err
	}
	for _, childID := range children {
		props, err := s.GetFolderProperties(childID, mapi.PrDisplayName)
		if err != nil {
			return err
		}
		childName, _ := stringProp(props, mapi.PrDisplayName)
		if _, err := s.copyFolderInto(childID, dstFolderID, childName, true); err != nil {
			return err
		}
	}
	return nil
}

// childFolderIDs returns the ids of a folder's direct, live subfolders.
func (s *Store) childFolderIDs(folderID int64) ([]int64, error) {
	rows, err := s.objdb.Query(`SELECT folder_id FROM folders WHERE parent_id=? AND is_deleted=0`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// FolderExists reports whether a live (non-deleted) folder with the given id
// exists in the object store.
func (s *Store) FolderExists(folderID int64) (bool, error) {
	var dummy int
	err := s.objdb.QueryRow(
		`SELECT 1 FROM folders WHERE folder_id=? AND is_deleted=0`, folderID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SetSubscribed sets a folder's IMAP subscription state, creating the folder's
// index row if it does not yet exist. It reports ErrNotFound when no such
// folder exists in the object store.
func (s *Store) SetSubscribed(folderID int64, subscribed bool) error {
	var dummy int
	err := s.objdb.QueryRow(
		`SELECT 1 FROM folders WHERE folder_id=? AND is_deleted=0`, folderID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	tx, err := s.idxdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.ensureIndexFolder(tx, folderID); err != nil {
		return err
	}
	unsub := 0
	if !subscribed {
		unsub = 1
	}
	if _, err := tx.Exec(`UPDATE folders SET unsub=? WHERE folder_id=?`, unsub, folderID); err != nil {
		return err
	}
	return tx.Commit()
}

// replicaGUID returns the mailbox replica GUID recorded at creation, used to
// stamp change keys on newly created folders.
func (s *Store) replicaGUID() (mapi.GUID, error) {
	str, err := s.storeGUID()
	if err != nil {
		return mapi.GUID{}, err
	}
	return mapi.ParseGUID(str)
}

// folderDisplayName returns a folder's PR_DISPLAY_NAME, or "" when unset.
func (s *Store) folderDisplayName(folderID int64) (string, error) {
	props, err := s.GetFolderProperties(folderID, mapi.PrDisplayName)
	if err != nil {
		return "", err
	}
	dn, _ := stringProp(props, mapi.PrDisplayName)
	return dn, nil
}

// folderSubscribed reports a folder's subscription state from the index. A
// folder with no index row yet is subscribed by default (unsub defaults to 0).
func (s *Store) folderSubscribed(folderID int64) (bool, error) {
	var unsub int
	err := s.idxdb.QueryRow(`SELECT unsub FROM folders WHERE folder_id=?`, folderID).Scan(&unsub)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return unsub == 0, nil
}

// descendants returns the non-deleted, non-search folders beneath root
// (excluding root itself), walking the parent links, ordered by id.
func (s *Store) descendants(root int64) ([]folderNode, error) {
	const q = `
		WITH RECURSIVE sub(folder_id, parent_id) AS (
			SELECT folder_id, parent_id FROM folders
				WHERE parent_id=? AND is_deleted=0 AND is_search=0
			UNION ALL
			SELECT f.folder_id, f.parent_id FROM folders f
				JOIN sub ON f.parent_id = sub.folder_id
				WHERE f.is_deleted=0 AND f.is_search=0
		)
		SELECT folder_id, parent_id FROM sub ORDER BY folder_id`
	rows, err := s.objdb.Query(q, root)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []folderNode
	for rows.Next() {
		var n folderNode
		var parent sql.NullInt64
		if err := rows.Scan(&n.id, &parent); err != nil {
			return nil, err
		}
		n.parent = parent.Int64
		out = append(out, n)
	}
	return out, rows.Err()
}

// folderSubtreeIDs returns folderID and all its descendant folder ids
// (deleted folders excluded), or an empty slice when the folder does not exist.
func (s *Store) folderSubtreeIDs(folderID int64) ([]int64, error) {
	const q = `
		WITH RECURSIVE sub(folder_id) AS (
			SELECT folder_id FROM folders WHERE folder_id=? AND is_deleted=0
			UNION ALL
			SELECT f.folder_id FROM folders f
				JOIN sub ON f.parent_id = sub.folder_id WHERE f.is_deleted=0
		)
		SELECT folder_id FROM sub`
	rows, err := s.objdb.Query(q, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// dropIndexFolder removes a folder's index rows, mappings, and cached eml
// files. A folder with no index rows (lazily created) is not an error.
func (s *Store) dropIndexFolder(folderID int64) error {
	rows, err := s.idxdb.Query(`SELECT message_id, mid_string FROM messages WHERE folder_id=?`, folderID)
	if err != nil {
		return err
	}
	type msg struct {
		id  int64
		mid string
	}
	var msgs []msg
	for rows.Next() {
		var m msg
		if err := rows.Scan(&m.id, &m.mid); err != nil {
			_ = rows.Close()
			return err
		}
		msgs = append(msgs, m)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range msgs {
		if _, err := s.idxdb.Exec(`DELETE FROM mapping WHERE message_id=?`, m.id); err != nil {
			return err
		}
	}
	if _, err := s.idxdb.Exec(`DELETE FROM messages WHERE folder_id=?`, folderID); err != nil {
		return err
	}
	if _, err := s.idxdb.Exec(`DELETE FROM folders WHERE folder_id=?`, folderID); err != nil {
		return err
	}
	for _, m := range msgs {
		s.removeEML(m.mid)
	}
	return nil
}

// boolProp reads a boolean-typed property, reporting false when absent or not a
// bool.
func boolProp(props mapi.PropertyValues, tag mapi.PropTag) bool {
	if v, ok := props.Get(tag); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// IsBuiltinFolder reports whether folderID is one the store provisions itself, so
// a surface can refuse an operation on one before deciding what to do with it.
func (s *Store) IsBuiltinFolder(folderID int64) bool { return s.isBuiltinFolder(folderID) }

// isBuiltinFolder reports whether folderID is one the store provisions itself.
// The private and public hierarchies number their folders separately and the
// ranges overlap, so the store's own kind decides which table to consult.
func (s *Store) isBuiltinFolder(folderID int64) bool {
	table := builtinFolders
	if s.kind == storePublic {
		table = builtinPublicFolders
	}
	for _, f := range table {
		// #nosec G115 -- every fid in these tables is a compile-time constant below PrivateFIDUnassignedStart, far inside int64
		if int64(f.fid) == folderID {
			return true
		}
	}
	return false
}

// dumpsterSubtree moves every message of the folders about to be removed into
// Recoverable Items, soft-deleted. Without it the folder row's cascade destroys
// the mail outright, which no other delete path in the store does.
//
// A public store has no Deleted Items, so there the messages go with the folder,
// as they did before.
func (s *Store) dumpsterSubtree(subtree []int64) error {
	if s.kind != storePrivate {
		return nil
	}
	trash := int64(mapi.PrivateFIDDeletedItems)
	for _, fid := range subtree {
		if fid == trash {
			continue // already where the mail is being moved to
		}
		ids, err := s.allFolderMessageIDs(fid)
		if err != nil {
			return err
		}
		for _, id := range ids {
			// Soft-delete first: it drops the index row and stamps PR_DELETED_ON,
			// which is what makes the message recoverable rather than merely moved.
			if err := s.SoftDeleteObject(id); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if _, err := s.objdb.Exec(
				`UPDATE messages SET parent_fid=? WHERE message_id=?`, trash, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// allFolderMessageIDs lists every message a folder holds, live or already in its
// own dumpster and of either associated class, because all of them are lost when
// the folder row goes.
func (s *Store) allFolderMessageIDs(folderID int64) ([]int64, error) {
	rows, err := s.objdb.Query(`SELECT message_id FROM messages WHERE parent_fid=?`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// FolderIsUnder reports whether folderID sits at or below ancestor, walking the
// STORED parent chain. A caller must not decide that from where it believes the
// folder is: the answer chooses between moving a folder to Deleted Items and
// removing it for good.
func (s *Store) FolderIsUnder(folderID, ancestor int64) (bool, error) {
	for id := folderID; id != 0; {
		if id == ancestor {
			return true, nil
		}
		var parent sql.NullInt64
		err := s.objdb.QueryRow(`SELECT parent_id FROM folders WHERE folder_id=?`, id).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, err
		}
		if !parent.Valid || parent.Int64 == id {
			return false, nil
		}
		id = parent.Int64
	}
	return false, nil
}
