package rop

import "slices"

// tableSignal is an open table's change baseline: the number of rows its folder
// held and the highest modification counter among them, both read in one
// aggregate query. A poll re-reads the pair, and any difference means the rows
// this table projects have moved. That is exactly what TABLE_CHANGED tells the
// client, so the table needs no per-row diff to raise it.
type tableSignal struct {
	count  int
	marker uint64
}

// readTableSignal reads the current baseline for one notifying table: the child
// folders for a hierarchy table, the messages of the live or the soft-deleted
// side for a contents table.
func readTableSignal(o *object) (tableSignal, error) {
	if o.table.kind == tableHierarchy {
		count, marker, err := o.store.FolderChildSignal(o.table.folderID)
		return tableSignal{count: count, marker: marker}, err
	}
	count, marker, err := o.store.FolderContentSignal(o.table.folderID, o.table.softDeleted)
	return tableSignal{count: count, marker: marker}, err
}

// enqueueTableChanges polls every open contents and hierarchy table and appends
// one TABLE_CHANGED per table whose folder moved since the last poll. Tables are
// visited in handle order for a deterministic batch.
//
// A table notification is addressed to the TABLE's own handle, not to a
// subscription: [MS-OXCNOTIF] 2.2.1.1 makes the table object its own notification
// target, which is why RopGetContentsTable and RopGetHierarchyTable carry a
// NoNotifications bit at all. A client therefore receives these without ever
// calling RopRegisterNotification.
//
// TABLE_CHANGED is the whole-table form: it names no row, so the client re-reads
// the table. hermEX raises that rather than the per-row TABLE_ROW_ADDED /
// TABLE_ROW_MODIFIED forms because a row event must carry the row's position in
// the client's current sort and restriction, and the table snapshot is frozen at
// open time, so a row added afterwards has no position in it.
//
// A store error skips that table without advancing its baseline, so the next poll
// retries the same comparison rather than losing the change.
func (s *Session) enqueueTableChanges() {
	tables := make([]uint32, 0)
	for h, o := range s.handles {
		if o.kind == kindTable && o.store != nil && o.table != nil && o.table.notify {
			tables = append(tables, h)
		}
	}
	slices.Sort(tables)
	for _, h := range tables {
		s.pollTable(h, s.handles[h])
	}
}

// pollTable compares one table's folder against the table's baseline and queues a
// TABLE_CHANGED when it moved. It refreshes the baseline either way, so one change
// raises exactly one event. A table with no baseline yet takes one here and reports
// nothing: the poll runs at the end of every Execute, including the one that opened
// the table, so the baseline is the folder's state at open time and rows the client
// already holds never raise an event.
func (s *Session) pollTable(handle uint32, o *object) {
	cur, err := readTableSignal(o)
	if err != nil {
		return
	}
	prev := o.table.signal
	o.table.signal = &cur
	if prev == nil || *prev == cur {
		return
	}
	s.pending = append(s.pending, queuedNotify{
		handle:  handle,
		logonID: 0, // a single logon in v1 (the dispatch discards the per-ROP LogonId)
		n:       notification{flags: fnevTableModified, tableEvent: tableChanged},
	})
}
