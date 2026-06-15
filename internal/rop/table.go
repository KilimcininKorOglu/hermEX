package rop

import (
	"bytes"
	"cmp"
	"slices"
	"strings"

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

// SortOrder Order values ([MS-OXCTABL] 2.2.1.3). v1 applies ascending/descending;
// the category orders (e.g. MAXIMUM_CATEGORY = 0x04) drive categorized tables,
// which RopSortTable rejects rather than silently flattening.
const (
	sortAscend  uint8 = 0x00
	sortDescend uint8 = 0x01
)

// sortKey is one resolved RopSortTable column: the property to order by and the
// direction.
type sortKey struct {
	tag        mapi.PropTag
	descending bool
}

// sortableType reports whether a property type has a defined sort order. Any other
// type (objects, restrictions, multivalue, ...) makes RopSortTable fail loud rather
// than return an unsorted table — the silent-error class this handler closes.
func sortableType(t mapi.PropType) bool {
	switch t {
	case mapi.PtShort, mapi.PtLong, mapi.PtCurrency, mapi.PtI8, mapi.PtSysTime,
		mapi.PtFloat, mapi.PtDouble, mapi.PtAppTime, mapi.PtBoolean,
		mapi.PtString8, mapi.PtUnicode, mapi.PtBinary:
		return true
	}
	return false
}

// compareValues orders two present property values. They are the Go types
// Push.PropValue emits for the column's type; a type mismatch (which the store
// should never produce) compares equal, so the stable sort keeps input order.
func compareValues(a, b any) int {
	switch x := a.(type) {
	case int16:
		if y, ok := b.(int16); ok {
			return cmp.Compare(x, y)
		}
	case int32:
		if y, ok := b.(int32); ok {
			return cmp.Compare(x, y)
		}
	case int64:
		if y, ok := b.(int64); ok {
			return cmp.Compare(x, y)
		}
	case uint64:
		if y, ok := b.(uint64); ok {
			return cmp.Compare(x, y)
		}
	case float32:
		if y, ok := b.(float32); ok {
			return cmp.Compare(x, y)
		}
	case float64:
		if y, ok := b.(float64); ok {
			return cmp.Compare(x, y)
		}
	case bool:
		if y, ok := b.(bool); ok {
			switch {
			case x == y:
				return 0
			case !x:
				return -1
			default:
				return 1
			}
		}
	case string:
		if y, ok := b.(string); ok {
			return strings.Compare(x, y)
		}
	case []byte:
		if y, ok := b.([]byte); ok {
			return bytes.Compare(x, y)
		}
	}
	return 0
}

// sortKeyBags fetches each base row's sort-key values, read independently of the
// column set since a client routinely sorts on a property it does not display.
// This projects every base row (an O(N) store walk, acceptable for v1's eager
// tables).
func (t *tableState) sortKeyBags(store *objectstore.Store) ([]mapi.PropertyValues, error) {
	tags := make([]mapi.PropTag, len(t.sortKeys))
	for i, k := range t.sortKeys {
		tags[i] = k.tag
	}
	n := t.baseCount()
	bags := make([]mapi.PropertyValues, n)
	for i := range n {
		switch t.kind {
		case tableHierarchy:
			b, err := store.GetFolderProperties(t.folders[i].ID, tags...)
			if err != nil {
				return nil, err
			}
			bags[i] = b
		case tableAttachment:
			bags[i] = t.attachments[i] // already in memory
		default:
			b, err := store.GetMessageProperties(t.messages[i].ID, tags...)
			if err != nil {
				return nil, err
			}
			bags[i] = b
		}
	}
	return bags, nil
}

// rebuildView recomputes the QueryRows view from the immutable base and resets the
// cursor to the beginning ([MS-OXCTABL]: RopSortTable repositions to row 0). With
// no sort keys the view is the identity (store order). A present value always sorts
// before an absent one, independent of direction, so a missing sort key is
// deterministic.
func (t *tableState) rebuildView(store *objectstore.Store) error {
	n := t.baseCount()
	view := make([]int, n)
	for i := range view {
		view[i] = i
	}
	if len(t.sortKeys) > 0 {
		bags, err := t.sortKeyBags(store)
		if err != nil {
			return err
		}
		slices.SortStableFunc(view, func(a, b int) int {
			for _, k := range t.sortKeys {
				av, aok := bags[a].Get(k.tag)
				bv, bok := bags[b].Get(k.tag)
				if !aok || !bok {
					if aok == bok {
						continue // both absent: tie on this key
					}
					if !aok {
						return 1 // a absent -> after b
					}
					return -1 // b absent -> a before b
				}
				c := compareValues(av, bv)
				if k.descending {
					c = -c
				}
				if c != 0 {
					return c
				}
			}
			return 0
		})
	}
	t.view = view
	t.cursor = 0
	return nil
}

// ropSortTable handles RopSortTable ([MS-OXCTABL] 2.2.2.3): it orders the table's
// rows by the requested columns and repositions the cursor. Categorized sorts and
// non-sortable column types are not implemented and fail loud rather than returning
// a wrongly-ordered table the client would trust.
func (s *Session) ropSortTable(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint8()         // TableFlags
	count, e2 := p.Uint16()    // SortOrderCount
	catCount, e3 := p.Uint16() // CategoryCount
	expanded, e4 := p.Uint16() // ExpandedCount
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	keys := make([]sortKey, 0, count)
	unsupported := false
	for range int(count) {
		ptype, ea := p.Uint16() // PropertyType
		pid, eb := p.Uint16()   // PropertyId
		order, ec := p.Uint8()  // Order
		if ea != nil || eb != nil || ec != nil {
			return false
		}
		desc := order == sortDescend
		if order != sortAscend && order != sortDescend {
			unsupported = true // a category order, not plain ascending/descending
		}
		if !sortableType(mapi.PropType(ptype)) {
			unsupported = true
		}
		keys = append(keys, sortKey{tag: mapi.MakeTag(pid, mapi.PropType(ptype)), descending: desc})
	}
	table := s.get(handleAt(handles, hindex))
	if table == nil || table.kind != kindTable || table.store == nil {
		writeErr(out, ropSortTable, hindex, ecError)
		return true
	}
	if catCount != 0 || expanded != 0 || unsupported {
		writeErr(out, ropSortTable, hindex, ecNotSupported)
		return true
	}
	table.table.sortKeys = keys
	if err := table.table.rebuildView(table.store); err != nil {
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
