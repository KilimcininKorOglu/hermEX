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
// a FLAGGED row (flag 0x01, then a FLAGGED_PROPVAL per column, available with
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

// openTable resolves a handle to a table object. ok is false when the handle
// names something else, in which case the caller's error response is written
// here rather than at each call site.
func (s *Session) openTable(out *ext.Push, ropID uint8, handles []uint32, hindex uint8, ec uint32) (*object, bool) {
	table := s.get(handleAt(handles, hindex))
	if table == nil || table.kind != kindTable {
		writeErr(out, ropID, hindex, ec)
		return nil, false
	}
	return table, true
}

// openStoreTable is openTable for the ROPs that reach the store: those also need
// the table bound to one.
func (s *Session) openStoreTable(out *ext.Push, ropID uint8, handles []uint32, hindex uint8) (*object, bool) {
	table, ok := s.openTable(out, ropID, handles, hindex, ecError)
	if !ok {
		return nil, false
	}
	if table.store == nil {
		writeErr(out, ropID, hindex, ecError)
		return nil, false
	}
	return table, true
}

// openProjectedTable is openStoreTable for the ROPs that project row data: those
// also need the column set chosen, since a table with no columns can project
// nothing.
func (s *Session) openProjectedTable(out *ext.Push, ropID uint8, handles []uint32, hindex uint8) (*object, bool) {
	table, ok := s.openStoreTable(out, ropID, handles, hindex)
	if !ok {
		return nil, false
	}
	if table.table.columns == nil {
		writeErr(out, ropID, hindex, ecError)
		return nil, false
	}
	return table, true
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
	table, ok := s.openProjectedTable(out, ropQueryRows, handles, hindex)
	if !ok {
		return true
	}
	ts := table.table
	total := ts.total()
	forward := forwardRead != 0

	idxs, newCursor := ts.pageIndices(int(rowCount), forward, total)
	rows, err := ts.encodeRows(table.store, idxs)
	if err != nil {
		writeErr(out, ropQueryRows, hindex, ecError)
		return true
	}
	if flags&queryRowsNoAdvance == 0 {
		ts.cursor = newCursor
	}
	seekPos := ts.seekPosition(forward, total)

	out.Uint8(ropQueryRows)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(seekPos)
	// #nosec G115 -- the length is bounded before it reaches the field, by the range check above it or by the 16-bit prefix the bytes were read with
	out.Uint16(uint16(len(idxs)))
	out.Raw(rows.Bytes())
	return true
}

// pullSeekRowRequest reads a RopSeekRow request. The offset is carried as an
// unsigned field but read as signed, and WantRowMovedCount is discarded because
// the response always reports the count.
func pullSeekRowRequest(p *ext.Pull) (seekPos uint8, offset uint32, ok bool) {
	seekPos, e1 := p.Uint8()
	offset, e2 := p.Uint32() // Offset, signed
	_, e3 := p.Uint8()       // WantRowMovedCount
	return seekPos, offset, e1 == nil && e2 == nil && e3 == nil
}

// pageIndices selects the row indices one RopQueryRows emits, in emit order,
// and the cursor position the page leaves behind. A backward read walks down
// from the row before the cursor, so the two directions never return the same
// row twice.
func (ts *tableState) pageIndices(rowCount int, forward bool, total int) ([]int, int) {
	if !forward {
		start := max(ts.cursor-rowCount, 0)
		var idxs []int
		for i := ts.cursor - 1; i >= start; i-- {
			idxs = append(idxs, i)
		}
		return idxs, start
	}
	end := min(ts.cursor+rowCount, total)
	var idxs []int
	for i := ts.cursor; i < end; i++ {
		idxs = append(idxs, i)
	}
	return idxs, end
}

// encodeRows projects each index as a PROPERTY_ROW over the table's columns,
// into one buffer.
func (ts *tableState) encodeRows(store *objectstore.Store, idxs []int) (*ext.Push, error) {
	rows := ext.NewPush(ext.FlagUTF16)
	for _, i := range idxs {
		props, err := ts.rowProps(store, i)
		if err != nil {
			return nil, err
		}
		if err := buildPropertyRow(rows, ts.columns, props); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// seekPosition reports where the cursor now stands, which the response carries
// so the client knows whether it reached an end of the table.
func (ts *tableState) seekPosition(forward bool, total int) uint8 {
	switch {
	case forward && ts.cursor >= total:
		return bookmarkEnd
	case !forward && ts.cursor == 0:
		return bookmarkBeginning
	}
	return bookmarkCurrent
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
	// #nosec G115 -- a Go slice length; the buffer it measures is orders of magnitude below the field
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
// than return an unsorted table, the silent-error class this handler closes.
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
	if r, ok := compareNumbers(a, b); ok {
		return r
	}
	if r, ok := compareTextual(a, b); ok {
		return r
	}
	return 0
}

// compareNumbers orders the integer and floating-point types. ok is false when
// a is not one of them, or when b is a different type.
func compareNumbers(a, b any) (int, bool) {
	switch x := a.(type) {
	case int16:
		return compareOrdered(x, b)
	case int32:
		return compareOrdered(x, b)
	case int64:
		return compareOrdered(x, b)
	case uint64:
		return compareOrdered(x, b)
	case float32:
		return compareOrdered(x, b)
	case float64:
		return compareOrdered(x, b)
	}
	return 0, false
}

// compareTextual orders the remaining sortable types: booleans (false before
// true), strings and binary.
func compareTextual(a, b any) (int, bool) {
	switch x := a.(type) {
	case bool:
		if y, ok := b.(bool); ok {
			return compareBool(x, y), true
		}
	case string:
		if y, ok := b.(string); ok {
			return strings.Compare(x, y), true
		}
	case []byte:
		if y, ok := b.([]byte); ok {
			return bytes.Compare(x, y), true
		}
	}
	return 0, false
}

// compareOrdered compares x against b when b holds the same ordered type.
func compareOrdered[T cmp.Ordered](x T, b any) (int, bool) {
	y, ok := b.(T)
	if !ok {
		return 0, false
	}
	return cmp.Compare(x, y), true
}

// compareBool orders false before true.
func compareBool(x, y bool) int {
	switch {
	case x == y:
		return 0
	case !x:
		return -1
	}
	return 1
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

// rebuildView recomputes the QueryRows view from the immutable base, filter, then
// sort, and resets the cursor to the beginning ([MS-OXCTABL]: RopRestrict and
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
	bags, err := t.keyProps(store, n)
	if err != nil {
		return err
	}
	view := make([]int, 0, n)
	for i := range n {
		if t.restriction == nil || evalRestriction(*t.restriction, bags[i]) {
			view = append(view, i)
		}
	}
	if len(t.sortKeys) > 0 {
		slices.SortStableFunc(view, func(a, b int) int {
			return t.compareRows(bags[a], bags[b])
		})
	}
	t.view = view
	return nil
}

// keyProps reads, for every base row, the properties the active filter and sort
// need. They are read once here rather than per comparison, because a sort
// touches a row many times.
func (t *tableState) keyProps(store *objectstore.Store, n int) ([]mapi.PropertyValues, error) {
	tags := t.viewTags()
	bags := make([]mapi.PropertyValues, n)
	for i := range n {
		b, err := t.rowKeyProps(store, i, tags)
		if err != nil {
			return nil, err
		}
		bags[i] = b
	}
	return bags, nil
}

// compareRows orders two rows by the table's sort keys, in key order. A row
// missing a key sorts after one that has it, and two rows both missing it tie on
// that key and fall through to the next.
func (t *tableState) compareRows(a, b mapi.PropertyValues) int {
	for _, k := range t.sortKeys {
		av, aok := a.Get(k.tag)
		bv, bok := b.Get(k.tag)
		if !aok || !bok {
			if c, tie := compareAbsent(aok, bok); !tie {
				return c
			}
			continue
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
}

// compareAbsent orders a pair where at least one side lacks the key. tie is true
// when both lack it, which leaves the decision to the next sort key.
func compareAbsent(aok, bok bool) (c int, tie bool) {
	if aok == bok {
		return 0, true // both absent: tie on this key
	}
	if !aok {
		return 1, false // a absent -> after b
	}
	return -1, false // b absent -> a before b
}

// ropSortTable handles RopSortTable ([MS-OXCTABL] 2.2.2.3): it orders the table's
// rows by the requested columns and repositions the cursor. Categorized sorts and
// non-sortable column types are not implemented and fail loud rather than returning
// a wrongly-ordered table the client would trust.
func (s *Session) ropSortTable(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	count, catCount, expanded, ok := pullSortHeader(p)
	if !ok {
		return false
	}
	keys, unsupported, framed := pullSortKeys(p, int(count))
	if !framed {
		return false
	}
	table, bound := s.openStoreTable(out, ropSortTable, handles, hindex)
	if !bound {
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

// pullSortHeader reads the fixed part of a RopSortTable request. The table flags
// are discarded; the category counts are returned because a non-zero one asks
// for a categorized table, which this server refuses rather than flattening.
func pullSortHeader(p *ext.Pull) (count, catCount, expanded uint16, ok bool) {
	_, e1 := p.Uint8()         // TableFlags
	count, e2 := p.Uint16()    // SortOrderCount
	catCount, e3 := p.Uint16() // CategoryCount
	expanded, e4 := p.Uint16() // ExpandedCount
	return count, catCount, expanded, e1 == nil && e2 == nil && e3 == nil && e4 == nil
}

// pullSortKeys reads the request's SortOrder array. unsupported reports a key
// this server cannot honour, which the caller answers with ecNotSupported rather
// than returning a wrongly-ordered table; framed is false when the array itself
// could not be read, which ends the batch.
func pullSortKeys(p *ext.Pull, count int) (keys []sortKey, unsupported, framed bool) {
	keys = make([]sortKey, 0, count)
	for range count {
		ptype, ea := p.Uint16() // PropertyType
		pid, eb := p.Uint16()   // PropertyId
		order, ec := p.Uint8()  // Order
		if ea != nil || eb != nil || ec != nil {
			return nil, false, false
		}
		if !sortOrderSupported(order, mapi.PropType(ptype)) {
			unsupported = true
		}
		keys = append(keys, sortKey{
			tag:        mapi.MakeTag(pid, mapi.PropType(ptype)),
			descending: order == sortDescend,
		})
	}
	return keys, unsupported, true
}

// sortOrderSupported reports whether one sort key can be honoured: a plain
// ascending or descending order (not a category order) over a type with a
// defined ordering.
func sortOrderSupported(order uint8, ptype mapi.PropType) bool {
	if order != sortAscend && order != sortDescend {
		return false // a category order, not plain ascending/descending
	}
	return sortableType(ptype)
}

// ropRestrict handles RopRestrict ([MS-OXCTABL] 2.2.2.4): it installs a filter so
// QueryRows returns only the matching rows. An empty restriction clears the filter.
// A restriction this server cannot evaluate fails loud (ecNotSupported) rather than
// returning an unfiltered table the client would trust, the silent-error class
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
	table, ok := s.openStoreTable(out, ropRestrict, handles, hindex)
	if !ok {
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
		return allRestrictionsSupported(kids)
	case mapi.ResNot:
		inner, _ := r.Value.(mapi.Restriction)
		return restrictionSupported(inner)
	case mapi.ResComment:
		c, _ := r.Value.(mapi.CommentRestriction)
		return c.Res == nil || restrictionSupported(*c.Res)
	case mapi.ResExist:
		return true
	case mapi.ResProperty:
		pr, _ := r.Value.(mapi.PropertyRestriction)
		return relopSupported(pr.Relop)
	case mapi.ResContent:
		c, _ := r.Value.(mapi.ContentRestriction)
		return contentRestrictionSupported(c)
	case mapi.ResBitmask:
		b, _ := r.Value.(mapi.BitmaskRestriction)
		return b.PropTag.Type() == mapi.PtLong
	}
	return false // compare-props, size, subobject, count, annotation, null
}

// allRestrictionsSupported reports whether every child of an AND or OR is one
// this server evaluates; one unsupported child makes the whole node unsupported.
func allRestrictionsSupported(kids []mapi.Restriction) bool {
	for _, k := range kids {
		if !restrictionSupported(k) {
			return false
		}
	}
	return true
}

// relopSupported reports whether a property restriction's relational operator is
// one this server evaluates. RelopRE (regular expression) and member-of-DL are
// not.
func relopSupported(relop mapi.Relop) bool {
	switch relop {
	case mapi.RelopLT, mapi.RelopLE, mapi.RelopGT, mapi.RelopGE, mapi.RelopEQ, mapi.RelopNE:
		return true
	}
	return false
}

// contentRestrictionSupported reports whether a content restriction is one this
// server evaluates: text-only, and a fuzzy level no stronger than IGNORECASE
// over full-string, substring or prefix matching.
func contentRestrictionSupported(c mapi.ContentRestriction) bool {
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
		return evalAll(kids, props)
	case mapi.ResOr:
		kids, _ := r.Value.([]mapi.Restriction)
		return evalAny(kids, props)
	case mapi.ResNot:
		inner, _ := r.Value.(mapi.Restriction)
		return !evalRestriction(inner, props)
	case mapi.ResComment:
		c, _ := r.Value.(mapi.CommentRestriction)
		return c.Res == nil || evalRestriction(*c.Res, props)
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

// evalAll is the AND node: every child must match. An empty AND matches.
func evalAll(kids []mapi.Restriction, props mapi.PropertyValues) bool {
	for _, k := range kids {
		if !evalRestriction(k, props) {
			return false
		}
	}
	return true
}

// evalAny is the OR node: one matching child is enough. An empty OR matches
// nothing.
func evalAny(kids []mapi.Restriction, props mapi.PropertyValues) bool {
	for _, k := range kids {
		if evalRestriction(k, props) {
			return true
		}
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
	// #nosec G115 -- the signed and unsigned views of the same 32 bits
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
	seekPos, off, framed := pullSeekRowRequest(p)
	if !framed {
		return false
	}
	table, ok := s.openTable(out, ropSeekRow, handles, hindex, ecError)
	if !ok {
		return true
	}
	ts := table.table
	total := ts.total()
	origin, valid := ts.seekOrigin(seekPos)
	if !valid {
		writeErr(out, ropSeekRow, hindex, ecError)
		return true
	}
	// #nosec G115 -- the signed and unsigned views of the same 32 bits
	offset := int32(off)
	// Both target and origin are clamped into [0, total], a Go slice length, so
	// their difference fits an int32 with room to spare.
	target := min(max(origin+int(offset), 0), total)
	ts.cursor = target
	// #nosec G115 -- target and origin are both in [0, total], a Go slice length, so the difference is far inside int32
	sought := int32(target - origin)
	var hasSoughtLess uint8
	if sought != offset {
		hasSoughtLess = 1
	}
	out.Uint8(ropSeekRow)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(hasSoughtLess)
	// #nosec G115 -- the signed and unsigned views of the same 32 bits, which is what RowsSought carries on the wire
	out.Uint32(uint32(sought))
	return true
}

// FindRow direction flag ([MS-OXCTABL] 2.2.2.13): forward (0x00) scans from the
// origin toward the end, backward toward the beginning.
const findRowBackward uint8 = 0x01

// matchRow reports whether the row at the given view position satisfies the
// restriction (a nil restriction matches every row), projecting the restriction's
// properties for that row independently of the column set.
func (t *tableState) matchRow(store *objectstore.Store, viewIdx int, r *mapi.Restriction) (bool, error) {
	if r == nil {
		return true, nil
	}
	props, err := t.rowKeyProps(store, t.baseIndex(viewIdx), restrictionTags(*r))
	if err != nil {
		return false, err
	}
	return evalRestriction(*r, props), nil
}

// ropFindRow handles RopFindRow ([MS-OXCTABL] 2.2.2.13): it scans the view from an
// origin (beginning/current/end) for the first row matching a restriction, moves
// the cursor there, and returns that row. With no match it reports HasRowData=0.
// The custom-bookmark origin needs bookmarks and is not yet supported.
func (s *Session) ropFindRow(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	req, ok := pullFindRowRequest(p)
	if !ok {
		return false
	}
	table, ok := s.openProjectedTable(out, ropFindRow, handles, hindex)
	if !ok {
		return true
	}
	ts := table.table
	restriction, ok := parseFindRowRestriction(out, hindex, req.restrictionData)
	if !ok {
		return true
	}
	start, ok := ts.seekOrigin(req.seekPos)
	if !ok {
		writeErr(out, ropFindRow, hindex, ecNotSupported) // custom bookmark / invalid
		return true
	}
	found, err := ts.scanForRow(table.store, start, restriction, req.flags&findRowBackward != 0)
	if err != nil {
		writeErr(out, ropFindRow, hindex, ecError)
		return true
	}

	var rowBytes []byte
	if found >= 0 {
		ts.cursor = found
		if rowBytes, err = ts.encodeRow(table.store, found); err != nil {
			writeErr(out, ropFindRow, hindex, ecError)
			return true
		}
	}

	out.Uint8(ropFindRow)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // RowNoLongerVisible (bookmark origin always valid here)
	if rowBytes == nil {
		out.Uint8(0) // HasRowData = 0: no row matched
		return true
	}
	out.Uint8(1) // HasRowData = 1
	out.Raw(rowBytes)
	return true
}

// findRowRequest is a decoded RopFindRow request. The bookmark is read but not
// kept: it applies only to the custom origin, which is deferred.
type findRowRequest struct {
	flags           uint8
	seekPos         uint8
	restrictionData []byte
}

func pullFindRowRequest(p *ext.Pull) (findRowRequest, bool) {
	var req findRowRequest
	flags, e1 := p.Uint8()
	resSize, e2 := p.Uint16()
	if e1 != nil || e2 != nil {
		return req, false
	}
	raw, e3 := p.Raw(int(resSize)) // RestrictionData
	if e3 != nil {
		return req, false
	}
	seekPos, e4 := p.Uint8()
	_, e5 := p.BinShort() // Bookmark (used only for the custom origin, deferred)
	if e4 != nil || e5 != nil {
		return req, false
	}
	return findRowRequest{flags: flags, seekPos: seekPos, restrictionData: raw}, true
}

// parseFindRowRestriction decodes the request's restriction, if it carries one.
// A nil restriction matches every row. ok=false means the response was already
// written.
func parseFindRowRestriction(out *ext.Push, hindex uint8, data []byte) (*mapi.Restriction, bool) {
	if len(data) == 0 {
		return nil, true
	}
	r, err := ext.NewPull(data, ext.FlagUTF16).Restriction()
	if err != nil {
		writeErr(out, ropFindRow, hindex, ecError)
		return nil, false
	}
	if !restrictionSupported(r) {
		writeErr(out, ropFindRow, hindex, ecNotSupported)
		return nil, false
	}
	return &r, true
}

// seekOrigin resolves a bookmark origin to the row index a scan starts from.
// ok=false for a custom or invalid bookmark, which this table does not serve.
func (ts *tableState) seekOrigin(seekPos uint8) (int, bool) {
	switch seekPos {
	case bookmarkBeginning:
		return 0, true
	case bookmarkCurrent:
		return ts.cursor, true
	case bookmarkEnd:
		return ts.total(), true
	}
	return 0, false
}

// scanForRow walks the table from start until a row matches the restriction,
// returning its index or -1 when none does. A store error stops the scan.
func (ts *tableState) scanForRow(store *objectstore.Store, start int, restriction *mapi.Restriction, backward bool) (int, error) {
	total := ts.total()
	step := 1
	if backward {
		// A backward scan starts at the row before the origin, so the origin's own
		// row is not re-examined.
		start, step = start-1, -1
	}
	for i := start; i >= 0 && i < total; i += step {
		match, err := ts.matchRow(store, i, restriction)
		if err != nil {
			return -1, err
		}
		if match {
			return i, nil
		}
	}
	return -1, nil
}

// encodeRow serializes one row as a PROPERTY_ROW over the table's columns.
func (ts *tableState) encodeRow(store *objectstore.Store, index int) ([]byte, error) {
	row, err := ts.rowProps(store, index)
	if err != nil {
		return nil, err
	}
	rp := ext.NewPush(ext.FlagUTF16)
	if err := buildPropertyRow(rp, ts.columns, row); err != nil {
		return nil, err
	}
	return rp.Bytes(), nil
}

// ropGetStatus handles RopGetStatus ([MS-OXCTABL] 2.2.2.9): it reports the table's
// asynchronous-operation status. hermEX builds every table synchronously, so the
// table is always fully populated: the status is TBLSTAT_COMPLETE.
func (s *Session) ropGetStatus(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, ok := s.openTable(out, ropGetStatus, handles, hindex, ecNotSupported)
	if !ok {
		return true
	}
	out.Uint8(ropGetStatus)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(tableStatusComplete)
	return true
}

// ropQueryPosition handles RopQueryPosition ([MS-OXCTABL] 2.2.2.5): it returns the
// cursor's current row index (Numerator) and the table's row count (Denominator).
func (s *Session) ropQueryPosition(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	table, ok := s.openTable(out, ropQueryPosition, handles, hindex, ecNotSupported)
	if !ok {
		return true
	}
	ts := table.table
	out.Uint8(ropQueryPosition)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	// #nosec G115 -- a row index in an in-memory table view
	out.Uint32(uint32(ts.cursor)) // Numerator
	// #nosec G115 -- a row index in an in-memory table view
	out.Uint32(uint32(ts.total())) // Denominator
	return true
}

// ropSeekRowFractional handles RopSeekRowFractional ([MS-OXCTABL] 2.2.2.17): it moves
// the cursor to the fractional position Numerator/Denominator of the table, computed
// as Numerator*total/Denominator. A zero Denominator is an invalid bookmark. The
// response carries no body beyond the return value.
func (s *Session) ropSeekRowFractional(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	numerator, e1 := p.Uint32()
	denominator, e2 := p.Uint32()
	if e1 != nil || e2 != nil {
		return false
	}
	table, ok := s.openTable(out, ropSeekRowFractional, handles, hindex, ecNotSupported)
	if !ok {
		return true
	}
	if denominator == 0 {
		writeErr(out, ropSeekRowFractional, hindex, ecInvalidBookmark)
		return true
	}
	ts := table.table
	// #nosec G115 -- int is 64-bit on every platform this builds for, so the value round-trips exactly
	pos := min(int(uint64(numerator)*uint64(ts.total())/uint64(denominator)), ts.total())
	ts.cursor = pos
	out.Uint8(ropSeekRowFractional)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}

// ropQueryColumnsAll handles RopQueryColumnsAll ([MS-OXCTABL] 2.2.2.3): it returns
// the property tags the table can produce. hermEX projects columns on demand with no
// fixed schema, so the answer is the currently configured display column set (empty
// before the first RopSetColumns).
func (s *Session) ropQueryColumnsAll(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	table, ok := s.openTable(out, ropQueryColumnsAll, handles, hindex, ecNotSupported)
	if !ok {
		return true
	}
	out.Uint8(ropQueryColumnsAll)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	_ = out.PropTags(table.table.columns) // PropertyTags (PROPTAG_ARRAY); nil => count 0
	return true
}

// ropAbort handles RopAbort ([MS-OXCTABL] 2.2.2.8): it would stop an in-progress
// asynchronous table build. hermEX builds tables synchronously, so there is never an
// operation to abort: the ROP fails with ecUnableToAbort, as Exchange does once the
// build has completed.
func (s *Session) ropAbort(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, ok := s.openTable(out, ropAbort, handles, hindex, ecNotSupported)
	if !ok {
		return true
	}
	writeErr(out, ropAbort, hindex, ecUnableToAbort)
	return true
}

// ropGetCollapseState handles RopGetCollapseState ([MS-OXCTABL] 2.2.2.18): it snapshots
// the expanded/collapsed state of a categorized table's headings. hermEX serves only
// flat (uncategorized) tables, which have no headings, so the ROP is not supported.
// The request body (row id + instance) is consumed so the ROP can sit anywhere in a
// batch, matching RopSetCollapseState's not-supported answer.
func (s *Session) ropGetCollapseState(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint64() // RowId
	_, e2 := p.Uint32() // RowInstanceNumber
	if e1 != nil || e2 != nil {
		return false
	}
	_, ok := s.openTable(out, ropGetCollapseState, handles, hindex, ecError)
	if !ok {
		return true
	}
	writeErr(out, ropGetCollapseState, hindex, ecNotSupported)
	return true
}

// ropFreeBookmark handles RopFreeBookmark ([MS-OXCTABL] 2.2.2.16): it releases a
// bookmark created by RopCreateBookmark. The bookmark blob is hermEX's own opaque
// 2-byte index (the form RopCreateBookmark emits); freeing an unknown or already-freed
// bookmark is a no-op, so the ROP always succeeds once the handle is a table.
func (s *Session) ropFreeBookmark(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	bk, e1 := p.BinShort() // Bookmark
	if e1 != nil {
		return false
	}
	table, ok := s.openTable(out, ropFreeBookmark, handles, hindex, ecError)
	if !ok {
		return true
	}
	ts := table.table
	if len(bk) >= 2 {
		idx := (uint16(bk[0]) << 8) | uint16(bk[1])
		if ts.bookmarks != nil {
			delete(ts.bookmarks, idx)
		}
	}
	out.Uint8(ropFreeBookmark)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}

// ropResetTable handles RopResetTable ([MS-OXCTABL] 2.2.2.14): it returns the table
// to its initial state, clearing the column set, sort order, restriction, and
// cursor, so the client starts a fresh SetColumns / Sort / Restrict cycle.
func (s *Session) ropResetTable(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	table, ok := s.openTable(out, ropResetTable, handles, hindex, ecError)
	if !ok {
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

// ensureBookmarks makes the bookmark map lazy on first use.
func (ts *tableState) ensureBookmarks() {
	if ts.bookmarks == nil {
		ts.bookmarks = make(map[uint16]int)
	}
}

// ropCreateBookmark handles RopCreateBookmark ([MS-OXCTABL] 2.2.2.1): it stores the
// current cursor position under a new bookmark index and returns that index as a
// BinShort. The bookmark persists until the table is released.
func (s *Session) ropCreateBookmark(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	table, ok := s.openTable(out, ropCreateBookmark, handles, hindex, ecError)
	if !ok {
		return true
	}
	ts := table.table
	ts.ensureBookmarks()
	idx := ts.nextBookmark
	ts.nextBookmark++
	ts.bookmarks[idx] = ts.cursor

	out.Uint8(ropCreateBookmark)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	_ = out.BinShort([]byte{byte(idx >> 8), byte(idx & 0xFF)}) // 2 bytes, so this never errors
	return true
}

// ropSeekRowBookmark handles RopSeekRowBookmark ([MS-OXCTABL] 2.2.2.4): it seeks
// relative to a stored bookmark the same way ropSeekRow seeks relative to
// BEGINNING/CURRENT/END. If the bookmark is not found it returns ecNotFound so the
// client can recreate it.
func (s *Session) ropSeekRowBookmark(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	bk, e1 := p.BinShort()   // Bookmark
	offset, e2 := p.Uint32() // Offset, signed
	_, e3 := p.Uint8()       // WantRowMovedCount
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	table, ok := s.openTable(out, ropSeekRowBookmark, handles, hindex, ecError)
	if !ok {
		return true
	}
	ts := table.table
	ts.ensureBookmarks()
	if len(bk) < 2 {
		writeErr(out, ropSeekRowBookmark, hindex, ecError)
		return true
	}
	idx := (uint16(bk[0]) << 8) | uint16(bk[1])
	origin, ok := ts.bookmarks[idx]
	if !ok {
		writeErr(out, ropSeekRowBookmark, hindex, ecNotFound)
		return true
	}
	total := ts.total()
	// #nosec G115 -- the signed and unsigned views of the same 32 bits
	off := int32(offset)
	target := origin + int(off)
	if target < 0 {
		target = 0
	} else if target > total {
		target = total
	}
	ts.cursor = target
	// #nosec G115 -- a row index in an in-memory table view
	sought := int32(target - origin)
	var hasSoughtLess uint8
	if sought != off {
		hasSoughtLess = 1
	}
	out.Uint8(ropSeekRowBookmark)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // RowInvisible, bookmark origin always valid
	out.Uint8(hasSoughtLess)
	// #nosec G115 -- the signed and unsigned views of the same 32 bits
	out.Uint32(uint32(sought))
	return true
}

// ropExpandRow handles RopExpandRow ([MS-OXCTABL] 2.2.2.8): it expands a collapsed
// category to show its child rows. Uncategorized (flat) tables have no categories,
// so this ROP always returns ecNotSupported. The body (MaxCount u32 + CategoryID u64)
// is NOT consumed here, this ROP must be alone in its batch.
func (s *Session) ropExpandRow(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, ok := s.openTable(out, ropExpandRow, handles, hindex, ecError)
	if !ok {
		return true
	}
	writeErr(out, ropExpandRow, hindex, ecNotSupported)
	return true
}

// ropCollapseRow handles RopCollapseRow ([MS-OXCTABL] 2.2.2.7): it collapses an
// expanded category to hide its child rows. Uncategorized (flat) tables have no
// categories, so this ROP always returns ecNotSupported. The body (CategoryID u64)
// is NOT consumed here, this ROP must be alone in its batch.
func (s *Session) ropCollapseRow(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, ok := s.openTable(out, ropCollapseRow, handles, hindex, ecError)
	if !ok {
		return true
	}
	writeErr(out, ropCollapseRow, hindex, ecNotSupported)
	return true
}

// ropSetCollapseState handles RopSetCollapseState ([MS-OXCTABL] 2.2.2.11): it sets
// the collapsed/expanded state of all categories in the table. Uncategorized
// (flat) tables have no categories, so this ROP always returns ecNotSupported.
// The body (collapse_state binary blob) is NOT consumed here, this ROP must be
// alone in its batch.
func (s *Session) ropSetCollapseState(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	table := s.get(handleAt(handles, hindex))
	if table == nil || table.kind != kindTable {
		writeErr(out, ropSetCollapseState, hindex, ecError)
		return true
	}
	writeErr(out, ropSetCollapseState, hindex, ecNotSupported)
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
