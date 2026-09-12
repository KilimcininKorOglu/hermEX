package objectstore

import (
	"slices"

	"hermex/internal/mapi"
)

// RepairReport is what RepairMailbox changed.
type RepairReport struct {
	Folders        int // folders whose IMAP index was reconciled
	ContentRemoved int // orphan content files reclaimed
}

// RepairMailbox reconciles what CheckMailbox can find without losing data: it
// rebuilds the IMAP index from the object store for every mail folder, then
// reclaims the content files no property references. It does NOT touch a damaged
// SQLite file, which is RecoverDatabase's job and loses rows.
//
// It holds the mailbox exclusively and reports ErrMailboxBusy rather than running
// while a daemon has the mailbox open, because reindexing rewrites index rows a
// live reader is addressing by UID.
func (s *Store) RepairMailbox() (RepairReport, error) {
	var report RepairReport
	err := s.withExclusiveLock(func() error {
		folders, err := s.mailFolderIDs()
		if err != nil {
			return err
		}
		for _, id := range folders {
			if err := s.ReindexFolder(id); err != nil {
				return err
			}
			report.Folders++
		}
		removed, err := s.sweepOrphanContent()
		report.ContentRemoved = removed
		return err
	})
	return report, err
}

// mailFolderIDs returns the object-store folders whose messages belong in the
// IMAP index, in ascending id order. The list comes from the OBJECT store rather
// than from the index, because an index that lost its rows is exactly the case a
// repair has to handle and an empty index would name no folder to repair.
//
// A folder qualifies when its PR_CONTAINER_CLASS is absent or IPF.Note. A
// calendar, contact, task or note folder carries its own class and is excluded,
// because its objects are stored with no index row at all and indexing them here
// would invent IMAP messages that never existed.
func (s *Store) mailFolderIDs() ([]int64, error) {
	all, err := idSet(s.objdb, `SELECT folder_id FROM folders`)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(all))
	for id := range all {
		props, err := s.GetFolderProperties(id, mapi.PrContainerClass)
		if err != nil {
			return nil, err
		}
		class, _ := stringProp(props, mapi.PrContainerClass)
		if class == "" || class == mapi.ContainerClassNote {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}
