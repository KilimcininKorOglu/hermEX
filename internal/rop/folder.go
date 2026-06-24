package rop

import (
	"slices"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// tableKind distinguishes the two table shapes a folder yields.
type tableKind uint8

const (
	tableContents   tableKind = iota // the folder's messages
	tableHierarchy                   // the folder's child folders
	tableAttachment                  // a message's attachments
	tablePermission                  // a folder's permission members
	tableRules                       // a folder's rules
)

// tableStatus values ([MS-OXCTABL] 2.2.2.1.3). v1 builds the table eagerly, so
// it is always complete.
const tableStatusComplete uint8 = 0x00

// tableState is the in-memory table a Get*Table ROP builds: a snapshot of the
// rows taken at creation plus the client's chosen column set and a forward
// cursor. The snapshot slices (messages/folders/attachments) are the immutable
// base; view holds the base-row indices in QueryRows order after RopSortTable /
// RopRestrict have been applied (nil means the identity order). Keeping the base
// immutable lets a later RopRestrict widen back to the full set. QueryRows pages
// over view, projecting the columns per row.
type tableState struct {
	kind         tableKind
	columns      []mapi.PropTag
	messages     []objectstore.MessageInfo // tableContents base rows
	folders      []objectstore.FolderInfo  // tableHierarchy base rows
	attachments  []mapi.PropertyValues     // tableAttachment base rows (attachment property bags)
	permissions  []mapi.PropertyValues     // tablePermission base rows (member property bags)
	rules        []mapi.PropertyValues     // tableRules base rows (rule property bags)
	sortKeys     []sortKey                 // RopSortTable order; empty = store order
	restriction  *mapi.Restriction         // RopRestrict filter; nil = no filter
	view         []int                     // base-row indices in display order; nil = identity
	cursor       int
	bookmarks    map[uint16]int // named cursor positions keyed by bookmark index
	nextBookmark uint16
}

// baseCount reports the immutable base row count for the table kind.
func (t *tableState) baseCount() int {
	switch t.kind {
	case tableHierarchy:
		return len(t.folders)
	case tableAttachment:
		return len(t.attachments)
	case tablePermission:
		return len(t.permissions)
	case tableRules:
		return len(t.rules)
	default:
		return len(t.messages)
	}
}

// total reports the number of rows QueryRows can page: the view length once a
// sort or restriction has been applied, else the full base.
func (t *tableState) total() int {
	if t.view != nil {
		return len(t.view)
	}
	return t.baseCount()
}

// baseIndex maps a view position to its immutable base-row index.
func (t *tableState) baseIndex(idx int) int {
	if t.view != nil {
		return t.view[idx]
	}
	return idx
}

// rowProps projects the column set for the row at idx from the store: a message
// property bag for a contents table, a folder property bag for a hierarchy one.
// The row identity (PrMid / PrFolderID) is the object's EID, not a stored
// property, so it is synthesized when requested — without it the client has no
// id to OpenMessage / OpenFolder the row it just found (the browse->open chain).
func (t *tableState) rowProps(store *objectstore.Store, idx int) (mapi.PropertyValues, error) {
	base := t.baseIndex(idx)
	if t.kind == tableHierarchy {
		fid := t.folders[base].ID
		props, err := store.GetFolderProperties(fid, t.columns...)
		if err != nil {
			return nil, err
		}
		if slices.Contains(t.columns, mapi.PrFolderID) {
			props.Set(mapi.PrFolderID, int64(mapi.MakeEIDEx(1, uint64(fid))))
		}
		return props, nil
	}
	if t.kind == tablePermission {
		// The member bags are built complete at GetPermissionsTable time and never
		// mutated, so the requested columns project straight from the snapshot.
		return t.permissions[base], nil
	}
	if t.kind == tableRules {
		// The rule bags are built complete at GetRulesTable time and never mutated.
		return t.rules[base], nil
	}
	if t.kind == tableAttachment {
		// The bags are already in memory; copy before synthesizing PR_ATTACH_NUM
		// so the stored snapshot is not mutated. A stored attach number is
		// authoritative; only when one is absent (legacy data that predates stored
		// numbers) is the base row index used as a fallback.
		row := append(mapi.PropertyValues(nil), t.attachments[base]...)
		if slices.Contains(t.columns, mapi.PrAttachNum) {
			if _, ok := row.Get(mapi.PrAttachNum); !ok {
				row.Set(mapi.PrAttachNum, int32(base))
			}
		}
		return row, nil
	}
	mid := t.messages[base].ID
	props, err := store.GetMessageProperties(mid, t.columns...)
	if err != nil {
		return nil, err
	}
	if slices.Contains(t.columns, mapi.PrMid) {
		props.Set(mapi.PrMid, int64(mapi.MakeEIDEx(1, uint64(mid))))
	}
	return props, nil
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
	// A delegate may open only a folder visible to them; an owner is unrestricted.
	if ok, err := s.authorize(parent.store, fid, mapi.FrightsVisible); err != nil {
		writeErr(out, ropOpenFolder, ohindex, ecError)
		return true
	} else if !ok {
		writeErr(out, ropOpenFolder, ohindex, ecAccessDenied)
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

// tableFlagSoftDeletes is the RopGetContentsTable TableFlags bit ([MS-OXCFOLD]
// 2.2.1.14.1) that asks for the Recoverable Items dumpster (soft-deleted items)
// instead of live mail. The server ANDs the raw wire byte with it directly.
const tableFlagSoftDeletes uint8 = 0x20

// ropGetContentsTable handles RopGetContentsTable ([MS-OXCFOLD] 2.2.1.14): it
// snapshots the folder's messages into a new table object and returns the row
// count. With the SHOW_SOFT_DELETES TableFlags bit set it snapshots the folder's
// soft-deleted (Recoverable Items) messages instead. The columns are chosen later
// via RopSetColumns; rows are read by RopQueryRows.
func (s *Session) ropGetContentsTable(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()    // OutputHandleIndex
	tableFlags, e2 := p.Uint8() // TableFlags
	if e1 != nil || e2 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropGetContentsTable, ohindex, ecError)
		return true
	}
	// Opening the folder required only Visible; reading its items requires ReadAny.
	if ok, err := s.authorize(folder.store, folder.folderID, mapi.FrightsReadAny); err != nil {
		writeErr(out, ropGetContentsTable, ohindex, ecError)
		return true
	} else if !ok {
		writeErr(out, ropGetContentsTable, ohindex, ecAccessDenied)
		return true
	}
	var (
		msgs []objectstore.MessageInfo
		err  error
	)
	if tableFlags&tableFlagSoftDeletes != 0 {
		msgs, err = folder.store.ListSoftDeletedInfo(folder.folderID)
	} else {
		msgs, err = folder.store.ListMessages(folder.folderID)
	}
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
