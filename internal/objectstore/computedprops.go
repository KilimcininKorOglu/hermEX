package objectstore

import (
	"database/sql"
	"errors"
	"math"

	"hermex/internal/mapi"
)

// Folder type and flag values ([MS-OXCFOLD] 2.2.2.2.1.5 and 2.2.2.2.2.7).
const (
	folderTypeRoot    int32 = 0
	folderTypeGeneric int32 = 1
	folderTypeSearch  int32 = 2

	folderFlagIPM    int32 = 1
	folderFlagSearch int32 = 2
	folderFlagNormal int32 = 4
	folderFlagRules  int32 = 8
)

// maxFolderDepth bounds the walk up a folder's ancestors, so a damaged parent
// chain that loops cannot hold a read forever.
const maxFolderDepth = 64

// FolderComputedProps returns the folder properties the store computes rather
// than stores ([MS-OXCFOLD] 2.2.2.2.1): the message and unread counts and the
// aggregate size of the folder's non-FAI messages, whether it has subfolders, its
// type, its flags and its id. A soft-deleted message is not counted.
func (s *Store) FolderComputedProps(fid int64) (mapi.PropertyValues, error) {
	count, unread, size, err := s.folderContentStats(fid)
	if err != nil {
		return nil, err
	}
	var subfolders int64
	if err := s.objdb.QueryRow(`SELECT COUNT(*) FROM folders WHERE parent_id=? AND is_deleted=0`, fid).Scan(&subfolders); err != nil {
		return nil, err
	}
	folderType, flags, parent, err := s.folderKind(fid)
	if err != nil {
		return nil, err
	}
	props := mapi.PropertyValues{
		{Tag: mapi.PrContentCount, Value: clampLong(count)},
		{Tag: mapi.PrContentUnreadCount, Value: clampLong(unread)},
		{Tag: mapi.PrMessageSizeExtended, Value: size},
		{Tag: mapi.PrMessageSize, Value: clampLong(size)},
		{Tag: mapi.PrSubfolders, Value: subfolders > 0},
		{Tag: mapi.PrFolderType, Value: folderType},
		{Tag: mapi.PrFolderFlags, Value: flags},
		{Tag: mapi.PrFolderID, Value: folderEID(fid)},
	}
	if parent.Valid {
		props = append(props, mapi.TaggedPropVal{Tag: mapi.PrParentFolderID, Value: folderEID(parent.Int64)})
	}
	return props, nil
}

// folderEID is a folder's short-term id as a PtI8 value.
func folderEID(fid int64) int64 {
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	return int64(mapi.MakeEIDEx(1, uint64(fid)))
}

// folderContentStats counts a folder's live non-FAI messages, the unread ones
// among them, and their total size.
func (s *Store) folderContentStats(fid int64) (count, unread, size int64, err error) {
	err = s.objdb.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN read_state=0 THEN 1 ELSE 0 END), 0), COALESCE(SUM(message_size), 0)
		 FROM messages WHERE parent_fid=? AND is_deleted=0 AND COALESCE(is_associated, 0)=0`, fid).Scan(&count, &unread, &size)
	return count, unread, size, err
}

// folderKind derives a folder's type and flags from its row, its ancestry and
// its rules, and returns its parent.
func (s *Store) folderKind(fid int64) (folderType, flags int32, parent sql.NullInt64, err error) {
	var isSearch int64
	if err := s.objdb.QueryRow(`SELECT parent_id, COALESCE(is_search, 0) FROM folders WHERE folder_id=?`, fid).Scan(&parent, &isSearch); err != nil {
		return 0, 0, parent, err
	}
	switch {
	case !parent.Valid:
		folderType = folderTypeRoot
	case isSearch != 0:
		folderType, flags = folderTypeSearch, folderFlagSearch
	default:
		folderType, flags = folderTypeGeneric, folderFlagNormal
	}
	ipm, err := s.underIPMSubtree(fid)
	if err != nil {
		return 0, 0, parent, err
	}
	if ipm {
		flags |= folderFlagIPM
	}
	var rules int64
	if err := s.objdb.QueryRow(`SELECT COUNT(*) FROM rules WHERE folder_id=?`, fid).Scan(&rules); err != nil {
		return 0, 0, parent, err
	}
	if rules > 0 {
		flags |= folderFlagRules
	}
	return folderType, flags, parent, nil
}

// underIPMSubtree reports whether a folder is the IPM subtree or lies below it.
func (s *Store) underIPMSubtree(fid int64) (bool, error) {
	id := fid
	for range maxFolderDepth {
		if id == int64(mapi.PrivateFIDIPMSubtree) {
			return true, nil
		}
		var parent sql.NullInt64
		err := s.objdb.QueryRow(`SELECT parent_id FROM folders WHERE folder_id=?`, id).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !parent.Valid) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		id = parent.Int64
	}
	return false, nil
}

// clampLong narrows a count or size to a PtLong, saturating at its maximum as the
// 32-bit PidTagMessageSize does past 2 GB.
func clampLong(v int64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v) // #nosec G115 -- bounded above and never negative
}
