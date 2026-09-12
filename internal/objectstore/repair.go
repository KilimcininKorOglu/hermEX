package objectstore

// RepairReport is what RepairMailbox changed.
type RepairReport struct {
	Folders        int // folders whose IMAP index was reconciled
	ContentRemoved int // orphan content files reclaimed
}

// RepairMailbox reconciles what CheckMailbox can find without losing data: it
// rebuilds the IMAP index from the object store for every indexed folder, then
// reclaims the content files no property references. It does NOT touch a damaged
// SQLite file, which is RecoverDatabase's job and loses rows.
//
// It holds the mailbox exclusively and reports ErrMailboxBusy rather than running
// while a daemon has the mailbox open, because reindexing rewrites index rows a
// live reader is addressing by UID.
func (s *Store) RepairMailbox() (RepairReport, error) {
	var report RepairReport
	err := s.withExclusiveLock(func() error {
		folders, err := idSet(s.idxdb, `SELECT folder_id FROM folders`)
		if err != nil {
			return err
		}
		for id := range folders {
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
