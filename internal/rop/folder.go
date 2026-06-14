package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// tableKind distinguishes the two table shapes a folder yields.
type tableKind uint8

const (
	tableContents  tableKind = iota // the folder's messages
	tableHierarchy                  // the folder's child folders
)

// tableStatus values ([MS-OXCTABL] 2.2.2.1.3). v1 builds the table eagerly, so
// it is always complete.
const tableStatusComplete uint8 = 0x00

// tableState is the in-memory table a Get*Table ROP builds: a snapshot of the
// rows taken at creation plus the client's chosen column set and a forward
// cursor. QueryRows pages over the snapshot, projecting the columns per row.
type tableState struct {
	kind     tableKind
	columns  []mapi.PropTag
	messages []objectstore.MessageInfo // tableContents rows
	folders  []objectstore.FolderInfo  // tableHierarchy rows
	cursor   int
}

// total reports the snapshot row count for either table kind.
func (t *tableState) total() int {
	if t.kind == tableHierarchy {
		return len(t.folders)
	}
	return len(t.messages)
}

// rowProps projects the column set for the row at idx from the store: a message
// property bag for a contents table, a folder property bag for a hierarchy one.
func (t *tableState) rowProps(store *objectstore.Store, idx int) (mapi.PropertyValues, error) {
	if t.kind == tableHierarchy {
		return store.GetFolderProperties(t.folders[idx].ID, t.columns...)
	}
	return store.GetMessageProperties(t.messages[idx].ID, t.columns...)
}

// ropOpenFolder handles RopOpenFolder ([MS-OXCFOLD] 2.2.1.1): it resolves the
// folder entry id against the store, registers a folder object at the output
// handle, and returns the (rules, ghost) flags. The 64-bit FolderId is an EID
// whose global-counter value is the objectstore folder id.
func (s *Session) ropOpenFolder(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()    // OutputHandleIndex
	folderEID, e2 := p.Uint64() // FolderId
	_, e3 := p.Uint8()          // OpenModeFlags
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	parent := s.get(handleAt(handles, hindex))
	if parent == nil || parent.store == nil {
		writeErr(out, ropOpenFolder, ohindex, ecError)
		return true
	}
	fid := int64(mapi.EID(folderEID).GCValue())
	exists, err := parent.store.FolderExists(fid)
	if err != nil {
		writeErr(out, ropOpenFolder, ohindex, ecError)
		return true
	}
	if !exists {
		writeErr(out, ropOpenFolder, ohindex, ecNotFound)
		return true
	}
	h := s.alloc(&object{kind: kindFolder, store: parent.store, folderID: fid})
	setHandle(handles, ohindex, h)

	out.Uint8(ropOpenFolder)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // HasRules
	out.Uint8(0) // HasGhostedRows (this is a private, non-ghosted folder)
	return true
}

// ropGetContentsTable handles RopGetContentsTable ([MS-OXCFOLD] 2.2.1.14): it
// snapshots the folder's messages into a new table object and returns the row
// count. The columns are chosen later via RopSetColumns; rows are read by
// RopQueryRows.
func (s *Session) ropGetContentsTable(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8() // OutputHandleIndex
	_, e2 := p.Uint8()       // TableFlags
	if e1 != nil || e2 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropGetContentsTable, ohindex, ecError)
		return true
	}
	msgs, err := folder.store.ListMessages(folder.folderID)
	if err != nil {
		writeErr(out, ropGetContentsTable, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{
		kind:  kindTable,
		store: folder.store,
		table: &tableState{kind: tableContents, messages: msgs},
	})
	setHandle(handles, ohindex, h)

	out.Uint8(ropGetContentsTable)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint32(uint32(len(msgs))) // RowCount
	return true
}

// ropSetColumns handles RopSetColumns ([MS-OXCTABL] 2.2.2.2): it stores the
// client's column proptag set on the table (used to type each value emitted by
// QueryRows) and reports the table complete. It operates on the table handle in
// place, so there is no output handle.
func (s *Session) ropSetColumns(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint8() // SetColumnsFlags
	cols, e2 := p.PropTags()
	if e1 != nil || e2 != nil {
		return false
	}
	table := s.get(handleAt(handles, hindex))
	if table == nil || table.kind != kindTable {
		writeErr(out, ropSetColumns, hindex, ecError)
		return true
	}
	table.table.columns = cols

	out.Uint8(ropSetColumns)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(tableStatusComplete) // TableStatus
	return true
}
