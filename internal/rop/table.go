package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// PROPERTY_ROW row flags ([MS-OXCDATA] 2.8.1): NONE when every requested column
// is present on the row, FLAGGED otherwise.
const (
	propertyRowNone    uint8 = 0x00
	propertyRowFlagged uint8 = 0x01
)

// QueryRows seek positions ([MS-OXCTABL]: BOOKMARK_BEGINNING/CURRENT/END are the
// OLE stream-seek values 0/1/2) and the no-advance request flag.
const (
	bookmarkBeginning uint8 = 0
	bookmarkCurrent   uint8 = 1
	bookmarkEnd       uint8 = 2

	queryRowsNoAdvance uint8 = 0x01
)

// buildPropertyRow serializes one PROPERTY_ROW against the column set
// ([MS-OXCDATA] 2.8.1, mirroring cu_propvals_to_row + p_proprow): a NONE row
// (flag 0x00, then a bare value per column) when every column is present, else
// a FLAGGED row (flag 0x01, then a FLAGGED_PROPVAL per column — available with
// its value, or unavailable). The column proptag types how each value encodes.
func buildPropertyRow(out *ext.Push, columns []mapi.PropTag, props mapi.PropertyValues) error {
	allPresent := true
	for _, col := range columns {
		if _, ok := props.Get(col); !ok {
			allPresent = false
			break
		}
	}
	if allPresent {
		out.Uint8(propertyRowNone)
		for _, col := range columns {
			v, _ := props.Get(col)
			if err := out.PropValue(col.Type(), v); err != nil {
				return err
			}
		}
		return nil
	}
	out.Uint8(propertyRowFlagged)
	for _, col := range columns {
		if v, ok := props.Get(col); ok {
			if err := out.FlaggedPropVal(col, mapi.FlaggedPropVal{Flag: mapi.FlaggedAvailable, Value: v}); err != nil {
				return err
			}
		} else if err := out.FlaggedPropVal(col, mapi.FlaggedPropVal{Flag: mapi.FlaggedUnavailable}); err != nil {
			return err
		}
	}
	return nil
}

// ropQueryRows handles RopQueryRows ([MS-OXCTABL] 2.2.2.5): it pages the table's
// row snapshot from the cursor, projects each row's columns from the store as a
// PROPERTY_ROW, advances the cursor (unless the no-advance flag is set), and
// frames SeekPosition + RowCount + the row bytes.
func (s *Session) ropQueryRows(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	flags, e1 := p.Uint8()
	forwardRead, e2 := p.Uint8()
	rowCount, e3 := p.Uint16()
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	table := s.get(handleAt(handles, hindex))
	if table == nil || table.kind != kindTable || table.store == nil || table.table.columns == nil {
		writeErr(out, ropQueryRows, hindex, ecError)
		return true
	}
	ts := table.table
	total := ts.total()
	forward := forwardRead != 0

	// Select the row indices to emit, in emit order, and the cursor they leave.
	var idxs []int
	newCursor := ts.cursor
	if forward {
		end := min(ts.cursor+int(rowCount), total)
		for i := ts.cursor; i < end; i++ {
			idxs = append(idxs, i)
		}
		newCursor = end
	} else {
		start := max(ts.cursor-int(rowCount), 0)
		for i := ts.cursor - 1; i >= start; i-- {
			idxs = append(idxs, i)
		}
		newCursor = start
	}

	rows := ext.NewPush(ext.FlagUTF16)
	for _, i := range idxs {
		props, err := ts.rowProps(table.store, i)
		if err != nil {
			writeErr(out, ropQueryRows, hindex, ecError)
			return true
		}
		if err := buildPropertyRow(rows, ts.columns, props); err != nil {
			writeErr(out, ropQueryRows, hindex, ecError)
			return true
		}
	}
	if flags&queryRowsNoAdvance == 0 {
		ts.cursor = newCursor
	}

	seekPos := bookmarkCurrent
	if forward {
		if ts.cursor >= total {
			seekPos = bookmarkEnd
		}
	} else if ts.cursor == 0 {
		seekPos = bookmarkBeginning
	}

	out.Uint8(ropQueryRows)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(seekPos)
	out.Uint16(uint16(len(idxs)))
	out.Raw(rows.Bytes())
	return true
}

// ropGetHierarchyTable handles RopGetHierarchyTable ([MS-OXCFOLD] 2.2.1.13): it
// snapshots the folder's direct children into a new hierarchy table and returns
// the row count.
func (s *Session) ropGetHierarchyTable(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8() // OutputHandleIndex
	_, e2 := p.Uint8()       // TableFlags
	if e1 != nil || e2 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropGetHierarchyTable, ohindex, ecError)
		return true
	}
	children, err := childFolders(folder.store, folder.folderID)
	if err != nil {
		writeErr(out, ropGetHierarchyTable, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{
		kind:  kindTable,
		store: folder.store,
		table: &tableState{kind: tableHierarchy, folders: children},
	})
	setHandle(handles, ohindex, h)

	out.Uint8(ropGetHierarchyTable)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint32(uint32(len(children))) // RowCount
	return true
}

// ropSortTable handles RopSortTable ([MS-OXCTABL] 2.2.2.3). v1 builds the table
// eagerly in default (store) order, so the sort is parsed (to consume the bytes
// and keep the batch aligned) but not applied; the table reports complete.
func (s *Session) ropSortTable(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint8()      // TableFlags
	count, e2 := p.Uint16() // SortOrderCount
	_, e3 := p.Uint16()     // CategoryCount
	_, e4 := p.Uint16()     // ExpandedCount
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	for i := 0; i < int(count); i++ {
		_, ea := p.Uint16() // PropertyType
		_, eb := p.Uint16() // PropertyId
		_, ec := p.Uint8()  // Order
		if ea != nil || eb != nil || ec != nil {
			return false
		}
	}
	if table := s.get(handleAt(handles, hindex)); table == nil || table.kind != kindTable {
		writeErr(out, ropSortTable, hindex, ecError)
		return true
	}
	out.Uint8(ropSortTable)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(tableStatusComplete)
	return true
}

// ropRestrict handles RopRestrict ([MS-OXCTABL] 2.2.2.4). As with SortTable, the
// restriction is consumed but not applied in v1 (no server-side filtering yet);
// the table reports complete.
func (s *Session) ropRestrict(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint8()        // RestrictFlags
	resSize, e2 := p.Uint16() // RestrictionDataSize
	if e1 != nil || e2 != nil {
		return false
	}
	if _, err := p.Raw(int(resSize)); err != nil { // RestrictionData (consumed verbatim)
		return false
	}
	if table := s.get(handleAt(handles, hindex)); table == nil || table.kind != kindTable {
		writeErr(out, ropRestrict, hindex, ecError)
		return true
	}
	out.Uint8(ropRestrict)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(tableStatusComplete)
	return true
}

// childFolders returns a folder's direct children from the user-visible tree.
// ListFolders reports the IPM subtree's own children with a nil ParentID, so a
// hierarchy table opened on the IPM subtree enumerates exactly those.
func childFolders(store *objectstore.Store, parentID int64) ([]objectstore.FolderInfo, error) {
	all, err := store.ListFolders()
	if err != nil {
		return nil, err
	}
	var out []objectstore.FolderInfo
	for _, f := range all {
		var isChild bool
		if f.ParentID == nil {
			isChild = parentID == int64(mapi.PrivateFIDIPMSubtree)
		} else {
			isChild = *f.ParentID == parentID
		}
		if isChild {
			out = append(out, f)
		}
	}
	return out, nil
}
