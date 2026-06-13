package objectstore

import (
	"database/sql"
	"errors"
	"os"
	"time"

	"hermex/internal/mapi"
)

// FolderInfo is the per-folder metadata needed to enumerate a mailbox's folder
// tree (e.g. for IMAP LIST). ParentID is nil for a top-level folder — one
// directly under the IPM subtree, which clients see as a mailbox root.
type FolderInfo struct {
	ID          int64
	ParentID    *int64
	DisplayName string
	Subscribed  bool
}

// ipmSubtree is the MAPI container holding a private store's user-visible
// folders. The client-facing folder tree is exactly this subtree: its direct
// children appear as top-level folders (ParentID nil) and deeper folders keep
// their real parent links. The root container and its sibling system folders
// (Views, Finder, Schedule, Spooler Queue, …) are MAPI-internal and never
// enumerated.
const ipmSubtree = int64(mapi.PrivateFIDIPMSubtree)

// folderNode is an (id, parent) pair from a hierarchy walk.
type folderNode struct {
	id     int64
	parent int64
}

// CreateFolder creates a folder under parent and returns its id. A nil parent
// places it at the top level — directly under the IPM subtree — which is where
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
	parentFID := ipmSubtree
	if parent != nil {
		parentFID = *parent
	}
	tx, err := s.objdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	fid, err := allocateEID(tx)
	if err != nil {
		return 0, err
	}
	begin, end, err := allocateRange(tx)
	if err != nil {
		return 0, err
	}
	cn, err := allocateCN(tx)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		`INSERT INTO folders (folder_id, parent_id, change_number, cur_eid, max_eid) VALUES (?, ?, ?, ?, ?)`,
		int64(fid), parentFID, int64(cn), int64(begin), int64(end)); err != nil {
		return 0, err
	}
	props, err := folderPropertyBag(tx, replica, mapi.UnixToNTTime(time.Now()), cn,
		displayName, mapi.ContainerClassNote, true, false)
	if err != nil {
		return 0, err
	}
	if err := insertProps(tx, "folder_properties", "folder_id", int64(fid), props); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(fid), nil
}

// FolderByName looks up a folder by parent and display name, reporting
// ok=false when none matches. A nil parent searches the top level (the IPM
// subtree's direct children). The name is matched against PR_DISPLAY_NAME.
func (s *Store) FolderByName(parent *int64, name string) (id int64, ok bool, err error) {
	parentFID := ipmSubtree
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
			rows.Close()
			return 0, false, err
		}
		ids = append(ids, fid)
	}
	rows.Close()
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
	nodes, err := s.descendants(ipmSubtree)
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
		if n.parent != ipmSubtree {
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
	parentFID := ipmSubtree
	if newParent != nil {
		parentFID = *newParent
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

// DeleteFolder removes a folder and its descendants: the object subtree (a
// foreign-key cascade drops child folders, messages, and property bags) and
// the matching index rows, mappings, and cached eml files. It reports
// ErrNotFound when no such folder exists.
func (s *Store) DeleteFolder(folderID int64) error {
	subtree, err := s.folderSubtreeIDs(folderID)
	if err != nil {
		return err
	}
	if len(subtree) == 0 {
		return ErrNotFound
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
	return nil
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
			rows.Close()
			return err
		}
		msgs = append(msgs, m)
	}
	rows.Close()
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
		_ = os.Remove(s.emlPath(m.mid))
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
