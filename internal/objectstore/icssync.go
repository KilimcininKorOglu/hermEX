package objectstore

import (
	"database/sql"
	"slices"

	"hermex/internal/ics"
	"hermex/internal/mapi"
)

// homeReplID is the private store's replica id for EID encoding. Replica id 1 is
// reserved (the replguidmap allocator hands out 6 and up), and the rop layer
// already wraps every folder/message id as MakeEIDEx(1, …) on the wire. The ics
// replica mapper therefore maps the store's own replica GUID to this id, so a
// client idset converted through it is keyed by 1 — the same space the diff
// below builds its lookup EIDs in.
const homeReplID uint16 = 1

// ContentSyncRequest configures a contents (message) synchronization diff over
// one folder ([MS-OXCFXICS] 3.3.5.13 GetContentsSync). Given holds the MIDs the
// client already has; Seen, SeenFAI, and Read hold the change numbers it has
// acknowledged for normal messages, FAI (folder-associated) messages, and read
// state.
//
// Each of Seen, SeenFAI, and Read must be non-nil IFF its SYNC class is enabled
// (SYNC_NORMAL / SYNC_ASSOCIATED / SYNC_READ_STATE); a nil set disables that
// class — normal or FAI messages of a disabled class are skipped entirely, and a
// nil Read suppresses read-state reporting. Given may be empty (an initial sync)
// but should be non-nil. Every idset must be in a loose, queryable form (post
// Convert) keyed by the home replica id.
type ContentSyncRequest struct {
	FolderID int64
	Given    *ics.IDSet
	Seen     *ics.IDSet
	SeenFAI  *ics.IDSet
	Read     *ics.IDSet
}

// ContentSyncResult is the computed contents delta. Every id is a bare
// home-replica GC value: a MID (message_id) in the *MIDs fields, a change number
// in LastCN / LastReadCN. The download path wraps each with mapi.MakeEIDEx(1, …)
// for the wire markers and re-reads the changed messages' full content.
type ContentSyncResult struct {
	ChangedMIDs  []uint64 // messages to (re)download: new or content-changed
	UpdatedMIDs  []uint64 // the subset of ChangedMIDs the client already had (a modification, not a create)
	GivenMIDs    []uint64 // the store's current in-scope MIDs — the client's new given set
	DeletedMIDs  []uint64 // given MIDs that no longer exist at all
	NoLongerMIDs []uint64 // given MIDs still stored but outside this sync's scope (e.g. FAI when only SYNC_NORMAL)
	ReadMIDs     []uint64 // body-up-to-date MIDs whose read state changed to read
	UnreadMIDs   []uint64 // body-up-to-date MIDs whose read state changed to unread
	LastCN       uint64   // the highest change number scanned (the client's new Seen high-water mark)
	LastReadCN   uint64   // the highest read change number scanned (the client's new Read high-water mark)
}

// GetContentSync computes the message delta for one folder against the client's
// prior synchronization state. It scans every live message, classifies each as
// up to date, content-changed, or read-state-changed against the client's given/
// seen/read idsets, then enumerates the given set to find deletions.
//
// Two branches are coded but dormant until the write path records their version:
// the "updated" (content-changed) branch fires only once a message-modification
// path bumps change_number, and the read-state branch fires only once a
// read-state change allocates a read_cn (the column is currently never written).
// Both are exercised here by constructed test state and faithful seeded data,
// and activate end to end when the download path's producers begin recording
// those versions. v1 reads only live (is_deleted=0) rows, so a soft-deleted
// given MID is reported as deleted rather than no-longer; the changed set is
// emitted in MID order, not delivery-time order. The given set is assumed to be
// a well-formed idset sized to a real mailbox.
func (s *Store) GetContentSync(req ContentSyncRequest) (ContentSyncResult, error) {
	var res ContentSyncResult
	rows, err := s.objdb.Query(
		`SELECT message_id, change_number, is_associated, read_state, read_cn
		   FROM messages WHERE parent_fid=? AND is_deleted=0`, req.FolderID)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	allMIDs := make(map[uint64]struct{})   // every live message in the folder
	existence := make(map[uint64]struct{}) // those in scope (passed class gating)
	type readChange struct {
		mid  uint64
		read bool
	}
	var readChanges []readChange

	for rows.Next() {
		var (
			mid, cn   int64
			assoc     sql.NullInt64
			readState int
			readCN    sql.NullInt64
		)
		if err := rows.Scan(&mid, &cn, &assoc, &readState, &readCN); err != nil {
			return res, err
		}
		umid, ucn := uint64(mid), uint64(cn)
		allMIDs[umid] = struct{}{}

		isFAI := assoc.Valid && assoc.Int64 != 0
		if isFAI {
			if req.SeenFAI == nil {
				continue
			}
		} else if req.Seen == nil {
			continue
		}
		existence[umid] = struct{}{}
		if ucn > res.LastCN {
			res.LastCN = ucn
		}
		if readCN.Valid && uint64(readCN.Int64) > res.LastReadCN {
			res.LastReadCN = uint64(readCN.Int64)
		}

		inGiven := req.Given != nil && req.Given.Contains(mapi.MakeEIDEx(homeReplID, umid))
		cnEID := mapi.MakeEIDEx(homeReplID, ucn)
		var seenCN bool
		if isFAI {
			seenCN = req.SeenFAI.Contains(cnEID)
		} else {
			seenCN = req.Seen.Contains(cnEID)
		}

		if inGiven && seenCN {
			// Body up to date. For a normal message, a read_cn the client has not
			// acknowledged is a read-state-only change.
			if !isFAI && req.Read != nil && readCN.Valid && readCN.Int64 != 0 &&
				!req.Read.Contains(mapi.MakeEIDEx(homeReplID, uint64(readCN.Int64))) {
				readChanges = append(readChanges, readChange{umid, readState != 0})
			}
			continue
		}
		res.ChangedMIDs = append(res.ChangedMIDs, umid)
		if inGiven {
			res.UpdatedMIDs = append(res.UpdatedMIDs, umid)
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}

	for mid := range existence {
		res.GivenMIDs = append(res.GivenMIDs, mid)
	}

	// Deletions: walk the client's given set. A foreign-replica id (replid > 1)
	// is unconditionally gone from this store; a home id absent from the in-scope
	// set is either still stored but out of scope (no-longer) or truly gone.
	if req.Given != nil {
		req.Given.ForEachRange(func(replid uint16, lo, hi uint64) {
			if replid != homeReplID {
				if replid > homeReplID {
					for v := lo; v <= hi; v++ {
						res.DeletedMIDs = append(res.DeletedMIDs, v)
					}
				}
				return
			}
			for v := lo; v <= hi; v++ {
				if _, ok := existence[v]; ok {
					continue
				}
				if _, ok := allMIDs[v]; ok {
					res.NoLongerMIDs = append(res.NoLongerMIDs, v)
				} else {
					res.DeletedMIDs = append(res.DeletedMIDs, v)
				}
			}
		})
	}

	for _, rc := range readChanges {
		if rc.read {
			res.ReadMIDs = append(res.ReadMIDs, rc.mid)
		} else {
			res.UnreadMIDs = append(res.UnreadMIDs, rc.mid)
		}
	}

	for _, set := range [][]uint64{
		res.ChangedMIDs, res.UpdatedMIDs, res.GivenMIDs,
		res.DeletedMIDs, res.NoLongerMIDs, res.ReadMIDs, res.UnreadMIDs,
	} {
		slices.Sort(set)
	}
	return res, nil
}

// HierarchySyncRequest configures a folder-hierarchy synchronization diff
// ([MS-OXCFXICS] 3.3.5.12 GetHierarchySync) over the subtree below FolderID.
// Given holds the FIDs the client already has; Seen holds the change numbers it
// has acknowledged. Both idsets must be loose and keyed by the home replica id.
type HierarchySyncRequest struct {
	FolderID int64
	Given    *ics.IDSet
	Seen     *ics.IDSet
}

// HierarchySyncResult is the computed hierarchy delta, with ids as bare
// home-replica GC values (folder_id in the *FIDs fields, a change number in
// LastCN). A hierarchy has no FAI, read state, or restriction, so there is no
// no-longer set: a given FID absent from the live subtree is reported deleted.
type HierarchySyncResult struct {
	ChangedFIDs []uint64 // subfolders to (re)download
	GivenFIDs   []uint64 // the store's current subtree FIDs — the client's new given set
	DeletedFIDs []uint64 // given FIDs no longer in the live subtree
	LastCN      uint64   // the highest change number scanned (the client's new Seen high-water mark)
}

// GetHierarchySync computes the subfolder delta for a folder subtree against the
// client's prior state. It walks every live descendant of FolderID, marks each
// changed unless the client both has the FID and has seen its change number, then
// enumerates the given set to find deletions. Like GetContentSync it reads only
// live (is_deleted=0) folders, so a soft-deleted given FID is reported deleted.
func (s *Store) GetHierarchySync(req HierarchySyncRequest) (HierarchySyncResult, error) {
	var res HierarchySyncResult
	rows, err := s.objdb.Query(
		`WITH RECURSIVE subtree(folder_id, change_number) AS (
			SELECT folder_id, change_number FROM folders WHERE parent_id=? AND is_deleted=0
			UNION ALL
			SELECT f.folder_id, f.change_number FROM folders f
			  JOIN subtree s ON f.parent_id = s.folder_id
			 WHERE f.is_deleted=0)
		 SELECT folder_id, change_number FROM subtree`, req.FolderID)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	existence := make(map[uint64]struct{})
	for rows.Next() {
		var fid, cn int64
		if err := rows.Scan(&fid, &cn); err != nil {
			return res, err
		}
		ufid, ucn := uint64(fid), uint64(cn)
		existence[ufid] = struct{}{}
		if ucn > res.LastCN {
			res.LastCN = ucn
		}
		inGiven := req.Given != nil && req.Given.Contains(mapi.MakeEIDEx(homeReplID, ufid))
		seenCN := req.Seen != nil && req.Seen.Contains(mapi.MakeEIDEx(homeReplID, ucn))
		if !(inGiven && seenCN) {
			res.ChangedFIDs = append(res.ChangedFIDs, ufid)
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}

	for fid := range existence {
		res.GivenFIDs = append(res.GivenFIDs, fid)
	}

	if req.Given != nil {
		req.Given.ForEachRange(func(replid uint16, lo, hi uint64) {
			if replid != homeReplID && replid <= homeReplID {
				return
			}
			for v := lo; v <= hi; v++ {
				if _, ok := existence[v]; !ok {
					res.DeletedFIDs = append(res.DeletedFIDs, v)
				}
			}
		})
	}

	slices.Sort(res.ChangedFIDs)
	slices.Sort(res.GivenFIDs)
	slices.Sort(res.DeletedFIDs)
	return res, nil
}
