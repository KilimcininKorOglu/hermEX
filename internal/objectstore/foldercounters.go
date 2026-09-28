package objectstore

import (
	"database/sql"

	"hermex/internal/mapi"
)

// A folder keeps two counters that only ever rise ([MS-OXCFOLD] 2.2.2.2.1.8 and
// 2.2.2.2.1.15): PidTagHierarchyChangeNumber counts the subfolders added to or
// removed from it, and PidTagDeletedCountTotal the messages deleted from it. They
// cannot be derived from what the store holds, because a purged message or a
// removed folder leaves no row, so every write that adds or removes one raises
// the count in the folder it leaves or enters. The value saturates at the PtLong
// maximum rather than wrapping.

// execer is what a counter update runs on: the database or a transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// countSubfolderChange raises the PidTagHierarchyChangeNumber of the folder a
// subfolder was added to or removed from.
func countSubfolderChange(ex execer, parentFID int64) error {
	_, err := ex.Exec(
		`INSERT INTO folder_properties (folder_id, proptag, propval) VALUES (?, ?, 1)
		 ON CONFLICT(folder_id, proptag) DO UPDATE SET propval = MIN(propval + 1, 2147483647)`,
		parentFID, int64(uint32(mapi.PrHierarchyChangeNum)))
	return err
}

// countMessageDeletion raises the PidTagDeletedCountTotal of the folder a live
// message is deleted from. A message already in the dumpster was counted when it
// was deleted, so purging it counts nothing.
func countMessageDeletion(ex execer, messageID int64) error {
	_, err := ex.Exec(
		`INSERT INTO folder_properties (folder_id, proptag, propval)
		 SELECT parent_fid, ?, 1 FROM messages WHERE message_id=? AND is_deleted=0 AND parent_fid IS NOT NULL
		 ON CONFLICT(folder_id, proptag) DO UPDATE SET propval = MIN(propval + 1, 2147483647)`,
		int64(uint32(mapi.PrDeletedCountTotal)), messageID)
	return err
}
