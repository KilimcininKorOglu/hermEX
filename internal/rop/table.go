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

// viewTags is every property the active filter and sort need, read independently
// of the display columns since a client routinely sorts or filters on a property
// it does not project.
func (t *tableState) viewTags() []mapi.PropTag {
	var tags []mapi.PropTag
	for _, k := range t.sortKeys {
		tags = append(tags, k.tag)
	}
	if t.restriction != nil {
		tags = append(tags, restrictionTags(*t.restriction)...)
	}
	return tags
}

// rowKeyProps fetches the given properties for one base row (an attachment bag is
// already in memory).
func (t *tableState) rowKeyProps(store *objectstore.Store, baseIdx int, tags []mapi.PropTag) (mapi.PropertyValues, error) {
	switch t.kind {
	case tableHierarchy:
		return store.GetFolderProperties(t.folders[baseIdx].ID, tags...)
	case tableAttachment:
		return t.attachments[baseIdx], nil
	default:
		return store.GetMessageProperties(t.messages[baseIdx].ID, tags...)
	}
}

// rebuildView recomputes the QueryRows view from the immutable base — filter, then
// sort — and resets the cursor to the beginning ([MS-OXCTABL]: RopRestrict and
// RopSortTable reposition to row 0). The filter and sort keys are projected per
// base row independently of the display columns (an O(N) store walk, acceptable for
// v1's eager tables). With neither a filter nor a sort the view is the identity
// (store order). A present value always sorts before an absent one, independent of
// direction, so a missing sort key is deterministic.
func (t *tableState) rebuildView(store *objectstore.Store) error {
	t.cursor = 0
	if t.restriction == nil && len(t.sortKeys) == 0 {
		t.view = nil // identity (store order)
		return nil
	}
	n := t.baseCount()
	tags := t.viewTags()
	bags := make([]mapi.PropertyValues, n)
	for i := range n {
		b, err := t.rowKeyProps(store, i, tags)
		if err != nil {
			return err
		}
		bags[i] = b
	}
	view := make([]int, 0, n)
	for i := range n {
		if t.restriction == nil || evalRestriction(*t.restriction, bags[i]) {
			view = append(view, i)
		}
	}
	if len(t.sortKeys) > 0 {
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

// ropRestrict handles RopRestrict ([MS-OXCTABL] 2.2.2.4): it installs a filter so
// QueryRows returns only the matching rows. An empty restriction clears the filter.
// A restriction this server cannot evaluate fails loud (ecNotSupported) rather than
// returning an unfiltered table the client would trust — the silent-error class
// this handler closes.
func (s *Session) ropRestrict(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint8()        // RestrictFlags
	resSize, e2 := p.Uint16() // RestrictionDataSize
	if e1 != nil || e2 != nil {
		return false
	}
	raw, e3 := p.Raw(int(resSize)) // RestrictionData
	if e3 != nil {
		return false
	}
	table := s.get(handleAt(handles, hindex))
	if table == nil || table.kind != kindTable || table.store == nil {
		writeErr(out, ropRestrict, hindex, ecError)
		return true
	}
	var restriction *mapi.Restriction
	if resSize > 0 {
		r, err := ext.NewPull(raw, ext.FlagUTF16).Restriction()
		if err != nil {
			writeErr(out, ropRestrict, hindex, ecError)
			return true
		}
		if !restrictionSupported(r) {
			writeErr(out, ropRestrict, hindex, ecNotSupported)
			return true
		}
		restriction = &r
	}
	table.table.restriction = restriction
	if err := table.table.rebuildView(table.store); err != nil {
		writeErr(out, ropRestrict, hindex, ecError)
		return true
	}
	out.Uint8(ropRestrict)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(tableStatusComplete)
	return true
}

// Content-restriction fuzzy level ([MS-OXCDATA] 2.12.3.1): the low 16 bits select
// the match kind, the high bits carry flags. v1 evaluates the three match kinds
// plus IGNORECASE.
const (
	fuzzyFullString uint32 = 0x0000
	fuzzySubString  uint32 = 0x0001
	fuzzyPrefix     uint32 = 0x0002
	fuzzyIgnoreCase uint32 = 0x00010000
)

// restrictionSupported reports whether the whole restriction tree is one this
// server evaluates. Anything outside the v1 subset (compare-properties, size,
// subobject, count, an unsupported relop, non-text content, or a content flag
// beyond IGNORECASE) makes RopRestrict fail loud rather than apply a partial
// filter.
func restrictionSupported(r mapi.Restriction) bool {
	switch r.Type {
	case mapi.ResAnd, mapi.ResOr:
		kids, _ := r.Value.([]mapi.Restriction)
		for _, k := range kids {
			if !restrictionSupported(k) {
				return false
			}
		}
		return true
	case mapi.ResNot:
		inner, _ := r.Value.(mapi.Restriction)
		return restrictionSupported(inner)
	case mapi.ResComment:
		c, _ := r.Value.(mapi.CommentRestriction)
		if c.Res == nil {
			return true
		}
		return restrictionSupported(*c.Res)
	case mapi.ResExist:
		return true
	case mapi.ResProperty:
		pr, _ := r.Value.(mapi.PropertyRestriction)
		switch pr.Relop {
		case mapi.RelopLT, mapi.RelopLE, mapi.RelopGT, mapi.RelopGE, mapi.RelopEQ, mapi.RelopNE:
			return true
		}
		return false // RelopRE / member-of-DL not evaluated
	case mapi.ResContent:
		c, _ := r.Value.(mapi.ContentRestriction)
		if c.PropTag.Type() != mapi.PtUnicode && c.PropTag.Type() != mapi.PtString8 {
			return false // v1 content matching is text-only
		}
		if c.FuzzyLevel&^(0xFFFF|fuzzyIgnoreCase) != 0 {
			return false // a fuzzy flag beyond IGNORECASE (IGNORENONSPACE / LOOSE)
		}
		switch c.FuzzyLevel & 0xFFFF {
		case fuzzyFullString, fuzzySubString, fuzzyPrefix:
			return true
		}
		return false
	case mapi.ResBitmask:
		b, _ := r.Value.(mapi.BitmaskRestriction)
		return b.PropTag.Type() == mapi.PtLong
	}
	return false // compare-props, size, subobject, count, annotation, null
}

// restrictionTags collects every property the restriction references, so
// rebuildView can project them per row independently of the display columns.
func restrictionTags(r mapi.Restriction) []mapi.PropTag {
	switch r.Type {
	case mapi.ResAnd, mapi.ResOr:
		kids, _ := r.Value.([]mapi.Restriction)
		var tags []mapi.PropTag
		for _, k := range kids {
			tags = append(tags, restrictionTags(k)...)
		}
		return tags
	case mapi.ResNot:
		inner, _ := r.Value.(mapi.Restriction)
		return restrictionTags(inner)
	case mapi.ResComment:
		c, _ := r.Value.(mapi.CommentRestriction)
		if c.Res == nil {
			return nil
		}
		return restrictionTags(*c.Res)
	case mapi.ResExist:
		e, _ := r.Value.(mapi.ExistRestriction)
		return []mapi.PropTag{e.PropTag}
	case mapi.ResProperty:
		pr, _ := r.Value.(mapi.PropertyRestriction)
		return []mapi.PropTag{pr.PropTag}
	case mapi.ResContent:
		c, _ := r.Value.(mapi.ContentRestriction)
		return []mapi.PropTag{c.PropTag}
	case mapi.ResBitmask:
		b, _ := r.Value.(mapi.BitmaskRestriction)
		return []mapi.PropTag{b.PropTag}
	}
	return nil
}

// evalRestriction reports whether a row's properties satisfy the restriction. It
// assumes restrictionSupported(r) held, so every node type here is one it handles.
func evalRestriction(r mapi.Restriction, props mapi.PropertyValues) bool {
	switch r.Type {
	case mapi.ResAnd:
		kids, _ := r.Value.([]mapi.Restriction)
		for _, k := range kids {
			if !evalRestriction(k, props) {
				return false
			}
		}
		return true
	case mapi.ResOr:
		kids, _ := r.Value.([]mapi.Restriction)
		for _, k := range kids {
			if evalRestriction(k, props) {
				return true
			}
		}
		return false
	case mapi.ResNot:
		inner, _ := r.Value.(mapi.Restriction)
		return !evalRestriction(inner, props)
	case mapi.ResComment:
		c, _ := r.Value.(mapi.CommentRestriction)
		if c.Res == nil {
			return true
		}
		return evalRestriction(*c.Res, props)
	case mapi.ResExist:
		e, _ := r.Value.(mapi.ExistRestriction)
		_, present := props.Get(e.PropTag)
		return present
	case mapi.ResProperty:
		return evalProperty(r.Value.(mapi.PropertyRestriction), props)
	case mapi.ResContent:
		return evalContent(r.Value.(mapi.ContentRestriction), props)
	case mapi.ResBitmask:
		return evalBitmask(r.Value.(mapi.BitmaskRestriction), props)
	}
	return false
}

// evalProperty applies a relational comparison between a row property and the
// restriction value. An absent property satisfies no comparison.
func evalProperty(pr mapi.PropertyRestriction, props mapi.PropertyValues) bool {
	v, ok := props.Get(pr.PropTag)
	if !ok {
		return false
	}
	c := compareValues(v, pr.PropVal.Value)
	switch pr.Relop {
	case mapi.RelopLT:
		return c < 0
	case mapi.RelopLE:
		return c <= 0
	case mapi.RelopGT:
		return c > 0
	case mapi.RelopGE:
		return c >= 0
	case mapi.RelopEQ:
		return c == 0
	case mapi.RelopNE:
		return c != 0
	}
	return false
}

// evalContent applies a text content match (full-string, substring, or prefix,
// optionally case-insensitive). An absent or non-text value matches nothing.
func evalContent(c mapi.ContentRestriction, props mapi.PropertyValues) bool {
	v, ok := props.Get(c.PropTag)
	if !ok {
		return false
	}
	row, ok1 := v.(string)
	want, ok2 := c.PropVal.Value.(string)
	if !ok1 || !ok2 {
		return false
	}
	if c.FuzzyLevel&fuzzyIgnoreCase != 0 {
		row, want = strings.ToLower(row), strings.ToLower(want)
	}
	switch c.FuzzyLevel & 0xFFFF {
	case fuzzySubString:
		return strings.Contains(row, want)
	case fuzzyPrefix:
		return strings.HasPrefix(row, want)
	default: // fuzzyFullString
		return row == want
	}
}

// evalBitmask tests masked bits of a PT_LONG property.
func evalBitmask(b mapi.BitmaskRestriction, props mapi.PropertyValues) bool {
	v, ok := props.Get(b.PropTag)
	if !ok {
		return false
	}
	n, ok := v.(int32)
	if !ok {
		return false
	}
	masked := uint32(n) & b.Mask
	switch b.Relop {
	case mapi.BmrEqz:
		return masked == 0
	case mapi.BmrNez:
		return masked != 0
	}
	return false
}

// ropSeekRow handles RopSeekRow ([MS-OXCTABL] 2.2.2.6): it moves the cursor by a
// signed offset from an origin (beginning/current/end), clamped to the view, and
// reports whether it stopped short and how many rows it actually moved.
func (s *Session) ropSeekRow(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	seekPos, e1 := p.Uint8()
	off, e2 := p.Uint32() // Offset, signed
	_, e3 := p.Uint8()    // WantRowMovedCount
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	table := s.get(handleAt(handles, hindex))
	if table == nil || table.kind != kindTable {
		writeErr(out, ropSeekRow, hindex, ecError)
		return true
	}
	ts := table.table
	total := ts.total()
	var origin int
	switch seekPos {
	case bookmarkBeginning:
		origin = 0
	case bookmarkCurrent:
		origin = ts.cursor
	case bookmarkEnd:
		origin = total
	default:
		writeErr(out, ropSeekRow, hindex, ecError)
		return true
	}
	offset := int32(off)
	target := origin + int(offset)
	if target < 0 {
		target = 0
	} else if target > total {
		target = total
	}
	ts.cursor = target
	sought := int32(target - origin)
	var hasSoughtLess uint8
	if sought != offset {
		hasSoughtLess = 1
	}
	out.Uint8(ropSeekRow)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(hasSoughtLess)
	out.Uint32(uint32(sought))
	return true
}

// ropResetTable handles RopResetTable ([MS-OXCTABL] 2.2.2.14): it returns the table
// to its initial state — clearing the column set, sort order, restriction, and
// cursor — so the client starts a fresh SetColumns / Sort / Restrict cycle.
func (s *Session) ropResetTable(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	table := s.get(handleAt(handles, hindex))
	if table == nil || table.kind != kindTable {
		writeErr(out, ropResetTable, hindex, ecError)
		return true
	}
	ts := table.table
	ts.columns = nil
	ts.sortKeys = nil
	ts.restriction = nil
	ts.view = nil
	ts.cursor = 0
	out.Uint8(ropResetTable)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
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
