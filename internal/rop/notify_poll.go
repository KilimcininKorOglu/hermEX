package rop

import (
	"slices"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// folderSnapshot maps a content folder's live objectstore message ids to their
// latest modification counter (MAX(change_number, read_cn)) — the baseline a
// notification poll diffs against.
type folderSnapshot map[int64]uint64

// folderMeta is a whole-store subscription's per-folder hierarchy baseline: the
// folder's container plus its live message counts. parentID is an objectstore id,
// already resolved to the IPM subtree root for a top-level folder, so a folder event
// never special-cases a sentinel. total and unread are the values a folder-modified
// event carries as NF_HAS_TOTAL / NF_HAS_UNREAD. A poll diffs this against the
// refreshed snapshot to detect folders created, deleted, or with changed counts.
type folderMeta struct {
	parentID int64
	total    uint32
	unread   uint32
}

// folderMetaSnapshot builds the per-folder hierarchy baseline for a whole-store
// subscription from an already-listed folder set: every content folder mapped to its
// parent (a top-level folder's parent resolved to the IPM subtree) and its live
// (total, unread) counts. It is taken at registration and refreshed each poll;
// detectFolderChanges diffs two snapshots into folder created/deleted/modified events.
func folderMetaSnapshot(st *objectstore.Store, folders []objectstore.FolderInfo) (map[int64]folderMeta, error) {
	out := make(map[int64]folderMeta, len(folders))
	for _, f := range folders {
		total, unread, err := st.CountMessages(f.ID)
		if err != nil {
			return nil, err
		}
		parent := int64(mapi.PrivateFIDIPMSubtree)
		if f.ParentID != nil {
			parent = *f.ParentID
		}
		out[f.ID] = folderMeta{parentID: parent, total: uint32(total), unread: uint32(unread)}
	}
	return out, nil
}

// detectFolderChanges diffs a whole-store subscription's previous folder-hierarchy
// snapshot against the current one and returns one folder-level notification per
// folder created, deleted, or modified. A folder absent from prev is a create
// (folder_id + parent_id); a folder absent from cur is a delete (folder_id + its old
// parent_id); a folder whose (total, unread) moved is a modify carrying both counts
// (NF_HAS_TOTAL|NF_HAS_UNREAD, and — per the wire gate — no parent_id). All events
// clear nfByMessage (they are folder-level), so no message id rides. Creates and
// modifies come first in folder-id order, then deletes in id order, for a
// deterministic batch. Folder counts are message-derived, so a new subfolder does not
// itself modify its parent here — its own create event signals it to the client.
//
// The modify trigger is count-only: a rename or other property change that leaves
// (total, unread) unchanged produces no event (folderMeta tracks no display name), and
// a folder move is the separate fnevObjectMoved increment. Both are deliberate
// deferrals — the events this emits are the ones a client needs to keep the tree's
// unread badges live.
func detectFolderChanges(prev, cur map[int64]folderMeta) []notification {
	var out []notification
	ids := make([]int64, 0, len(cur))
	for id := range cur {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		c := cur[id]
		fid := uint64(mapi.MakeEIDEx(1, uint64(id)))
		switch p, ok := prev[id]; {
		case !ok:
			out = append(out, notification{
				flags:    fnevObjectCreated,
				folderID: fid,
				parentID: uint64(mapi.MakeEIDEx(1, uint64(c.parentID))),
			})
		case p.total != c.total || p.unread != c.unread:
			out = append(out, notification{
				flags:       fnevObjectModified | nfHasTotal | nfHasUnread,
				folderID:    fid,
				totalCount:  c.total,
				unreadCount: c.unread,
			})
		}
	}
	var deleted []int64
	for id := range prev {
		if _, ok := cur[id]; !ok {
			deleted = append(deleted, id)
		}
	}
	slices.Sort(deleted)
	for _, id := range deleted {
		out = append(out, notification{
			flags:    fnevObjectDeleted,
			folderID: uint64(mapi.MakeEIDEx(1, uint64(id))),
			parentID: uint64(mapi.MakeEIDEx(1, uint64(prev[id].parentID))),
		})
	}
	return out
}

// detectContentChanges diffs a content folder's previous snapshot against its
// current state and returns one message notification per change, plus the refreshed
// snapshot to carry into the next poll. A new id is a create, a changed counter a
// modify, and a vanished id a delete (the counter does not advance on a hard delete,
// so a delete is seen as the id's absence — see the internal spec §9).
// Changes are ordered by id (creates and modifies, then deletes) for a deterministic
// batch. folderID is the objectstore id; the notifications carry wire EIDs. Reading
// only the shared store makes this multi-process safe: it sees a change any daemon
// made, which is how hermEX delivers notifications without a central store daemon to
// push from.
func detectContentChanges(st *objectstore.Store, folderID int64, prev folderSnapshot) ([]notification, folderSnapshot, error) {
	cur, err := st.FolderMessageChangeNumbers(folderID)
	if err != nil {
		return nil, nil, err
	}
	fid := uint64(mapi.MakeEIDEx(1, uint64(folderID)))

	ids := make([]int64, 0, len(cur))
	for id := range cur {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	var out []notification
	for _, id := range ids {
		mid := uint64(mapi.MakeEIDEx(1, uint64(id)))
		switch prevCN, ok := prev[id]; {
		case !ok:
			out = append(out, notification{flags: fnevObjectCreated | nfByMessage, folderID: fid, messageID: mid})
		case prevCN != cur[id]:
			out = append(out, notification{flags: fnevObjectModified | nfByMessage, folderID: fid, messageID: mid})
		}
	}

	var deleted []int64
	for id := range prev {
		if _, ok := cur[id]; !ok {
			deleted = append(deleted, id)
		}
	}
	slices.Sort(deleted)
	for _, id := range deleted {
		mid := uint64(mapi.MakeEIDEx(1, uint64(id)))
		out = append(out, notification{flags: fnevObjectDeleted | nfByMessage, folderID: fid, messageID: mid})
	}
	return out, cur, nil
}
