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
