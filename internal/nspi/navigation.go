package nspi

import (
	"slices"
	"sort"
	"strings"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// SeekEntries handles the NSPI SeekEntries request ([MS-OXNSPI] 2.2.4): it
// positions the cursor at the first entry whose display name is at or after a
// target value, returning that entry's row. v1 supports the display-name sort
// (the only sort an online client uses for the GAL). With an explicit MId list
// the search runs in that list's order; otherwise it binary-searches the
// display-name-ordered GAL — the same comparison snapshot() sorts by.
func (s *Server) SeekEntries(body []byte) []byte {
	req, err := pullSeekEntries(body)
	if err != nil {
		return s.encodeSeekEntries(ecError, stat{}, nil, nil)
	}
	r := s.seekEntriesCore(req)
	return s.encodeSeekEntries(r.result, r.stat, r.cols, r.rows)
}

// seekEntriesCore runs the SeekEntries semantics on a decoded request,
// transport-neutral: the MAPI/HTTP handler and the RPC/HTTP stub share it.
func (s *Server) seekEntriesCore(req seekEntriesRequest) rowsetResult {
	st := req.stat
	if st.codePage == cpWinUnicode || req.reserved != 0 {
		return rowsetResult{result: ecNotSupported, stat: st}
	}
	if !isDisplayNameSort(st.sortType) {
		return rowsetResult{result: ecError, stat: st}
	}
	// The target seeks on the display name; accept either the Unicode or the
	// ANSI variant (same property ID, the reference checks both).
	if req.target.Tag.ID() != mapi.PrDisplayName.ID() {
		return rowsetResult{result: ecError, stat: st}
	}
	target, _ := req.target.Value.(string)
	cols := req.columns
	if !req.hasCols || len(cols) == 0 {
		cols = defaultColumns
	}
	if len(cols) > 100 {
		return rowsetResult{result: ecTableTooBig, stat: st}
	}

	g := s.snapshot()
	var found galUser
	var pos, total int
	var ok bool
	if req.table != nil {
		// An explicit MId list is the client's own selection: scan it directly,
		// unfiltered, over the full snapshot.
		found, pos, ok = g.seekTable(target, req.table)
		total = len(g.users)
	} else {
		// A plain seek walks the view the container selected (the GAL browse view,
		// or a named list's type view), so entries hidden on that surface are not
		// landed on.
		view := g.viewFor(st.containerID)
		found, pos, ok = view.seek(target)
		total = view.total()
	}
	if !ok {
		return rowsetResult{result: ecNotFound, stat: st}
	}
	st.curRec = found.mid
	st.numPos = uint32(pos)
	st.totalRec = uint32(total)
	rows := []mapi.PropertyValues{galUserProps(found)}
	return rowsetResult{result: ecSuccess, stat: st, cols: cols, rows: rows}
}

// seekTable positions at the first entry of a client-supplied MId list whose
// display name is >= target, scanning in the list's order. The list is the
// client's own selection (a direct reference), so hidden entries are not
// filtered. The returned position is the entry's full-snapshot GAL index.
func (g gal) seekTable(target string, table []uint32) (galUser, int, bool) {
	t := strings.ToLower(target)
	for _, mid := range table {
		if u, ok := g.byMID(mid); ok && strings.ToLower(u.display) >= t {
			return u, int(mid - midBase), true
		}
	}
	return galUser{}, 0, false
}

// seek binary-searches the GAL-browse view for the first entry whose display
// name is >= target (case-insensitively, the order snapshot() sorts by). The
// view is the display-ordered subsequence visible in the GAL, so the returned
// position is a visible-space index.
func (v galView) seek(target string) (galUser, int, bool) {
	t := strings.ToLower(target)
	i := sort.Search(v.total(), func(i int) bool {
		return strings.ToLower(v.userAt(i).display) >= t
	})
	if i >= v.total() {
		return galUser{}, 0, false
	}
	return v.userAt(i), i, true
}

// seekEntriesRequest is the decoded SeekEntries body ([MS-OXNSPI] 2.2.4): a
// reserved word, an optional STAT, the target value to seek, an optional
// explicit MId list to seek within, and an optional column set.
type seekEntriesRequest struct {
	reserved uint32
	stat     stat
	target   mapi.TaggedPropVal
	table    []uint32
	columns  []mapi.PropTag
	hasCols  bool
}

func pullSeekEntries(body []byte) (seekEntriesRequest, error) {
	p := ext.NewPull(body, abkFlags)
	var r seekEntriesRequest
	var err error
	if r.reserved, err = p.Uint32(); err != nil {
		return r, err
	}
	hasStat, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasStat != 0 {
		if r.stat, err = pullStat(p); err != nil {
			return r, err
		}
	}
	hasTarget, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasTarget != 0 {
		if r.target, err = p.TaggedPropVal(); err != nil {
			return r, err
		}
	}
	hasTable, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasTable != 0 {
		tags, terr := p.PropTagsLong()
		if terr != nil {
			return r, terr
		}
		r.table = make([]uint32, len(tags))
		for i, t := range tags {
			r.table[i] = uint32(t)
		}
	}
	hasCols, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasCols != 0 {
		r.hasCols = true
		if r.columns, err = p.PropTagsLong(); err != nil {
			return r, err
		}
	}
	return r, skipAuxIn(p)
}

// encodeSeekEntries frames a SeekEntries response: status + result + the updated
// STAT (always present) + the row set on success (else a single 0), then an
// empty AuxiliaryBuffer.
func (s *Server) encodeSeekEntries(result uint32, st stat, cols []mapi.PropTag, rows []mapi.PropertyValues) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)      // status
	p.Uint32(result) // result
	p.Uint8(0xFF)    // STAT present (always)
	pushStat(p, st)
	if result != ecSuccess {
		p.Uint8(0)
	} else {
		p.Uint8(0xFF)
		_ = pushColRow(p, cols, rows)
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}

// CompareMids handles the NSPI CompareMids request ([MS-OXNSPI] 2.2.4): it
// returns the relative table order of two MIds. Because our MId encodes the
// entry's display-name position, the comparison is the position difference. Both
// MIds must exist, else the comparison is an error.
func (s *Server) CompareMids(body []byte) []byte {
	req, err := pullCompareMids(body)
	if err != nil {
		return s.encodeCompareMids(ecError, 0)
	}
	r := s.compareMidsCore(req)
	return s.encodeCompareMids(r.result, r.cmp)
}

// compareMidsResult is the transport-neutral outcome of CompareMids: a result
// code and the signed table-order comparison of the two MIds.
type compareMidsResult struct {
	result uint32
	cmp    int32
}

// compareMidsCore runs the CompareMids semantics on a decoded request,
// transport-neutral: the MAPI/HTTP handler and the RPC/HTTP stub share it.
func (s *Server) compareMidsCore(req compareMidsRequest) compareMidsResult {
	if req.stat.codePage == cpWinUnicode {
		return compareMidsResult{result: ecNotSupported}
	}
	g := s.snapshot()
	_, ok1 := g.byMID(req.mid1)
	_, ok2 := g.byMID(req.mid2)
	if !ok1 || !ok2 {
		return compareMidsResult{result: ecError}
	}
	p1, p2 := g.position(req.mid1), g.position(req.mid2)
	var cmp int32
	switch {
	case p2 < p1:
		cmp = -1
	case p2 > p1:
		cmp = 1
	}
	return compareMidsResult{result: ecSuccess, cmp: cmp}
}

// compareMidsRequest is the decoded CompareMids body ([MS-OXNSPI] 2.2.4): a
// reserved word, an optional STAT, and the two MIds to compare.
type compareMidsRequest struct {
	stat       stat
	mid1, mid2 uint32
}

func pullCompareMids(body []byte) (compareMidsRequest, error) {
	p := ext.NewPull(body, abkFlags)
	var r compareMidsRequest
	if _, err := p.Uint32(); err != nil { // reserved
		return r, err
	}
	hasStat, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasStat != 0 {
		if r.stat, err = pullStat(p); err != nil {
			return r, err
		}
	}
	if r.mid1, err = p.Uint32(); err != nil {
		return r, err
	}
	if r.mid2, err = p.Uint32(); err != nil {
		return r, err
	}
	return r, skipAuxIn(p)
}

// encodeCompareMids frames a CompareMids response: status + the comparison
// result + the return code + an empty AuxiliaryBuffer. The comparison precedes
// the result and is always present.
func (s *Server) encodeCompareMids(result uint32, cmp int32) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)           // status
	p.Uint32(uint32(cmp)) // comparison result (signed)
	p.Uint32(result)      // result
	p.Uint32(0)           // AuxiliaryBufferSize
	return p.Bytes()
}

// ResortRestriction handles the NSPI ResortRestriction request ([MS-OXNSPI]
// 2.2.4): it sorts a client-supplied MId list into display-name order. Because
// our MId encodes the display-name position, the sort is numeric on the MIds.
// Non-existent MIds are dropped; if the STAT's current record is no longer in
// the set, the cursor resets to the table start.
func (s *Server) ResortRestriction(body []byte) []byte {
	req, err := pullResortRestriction(body)
	if err != nil {
		return s.encodeResortRestriction(ecError, stat{}, nil)
	}
	r := s.resortRestrictionCore(req)
	return s.encodeResortRestriction(r.result, r.stat, r.mids)
}

// resortResult is the transport-neutral outcome of ResortRestriction: a result
// code, the updated cursor, and the reordered MId list.
type resortResult struct {
	result uint32
	stat   stat
	mids   []uint32
}

// resortRestrictionCore runs the ResortRestriction semantics on a decoded
// request, transport-neutral: the MAPI/HTTP handler and the RPC/HTTP stub share
// it.
func (s *Server) resortRestrictionCore(req resortRestrictionRequest) resortResult {
	st := req.stat
	if st.codePage == cpWinUnicode {
		return resortResult{result: ecNotSupported, stat: st}
	}
	g := s.snapshot()
	var out []uint32
	found := false
	for _, mid := range req.inmids {
		if _, ok := g.byMID(mid); ok {
			out = append(out, mid)
			if mid == st.curRec {
				found = true
			}
		}
	}
	slices.Sort(out) // ascending MId == display-name order
	st.totalRec = uint32(len(out))
	if !found {
		st.curRec = midBeginningOfTable
		st.numPos = 0
	}
	return resortResult{result: ecSuccess, stat: st, mids: out}
}

// resortRestrictionRequest is the decoded ResortRestriction body ([MS-OXNSPI]
// 2.2.4): a reserved word, an optional STAT, and the MId list to reorder.
type resortRestrictionRequest struct {
	stat   stat
	inmids []uint32
}

func pullResortRestriction(body []byte) (resortRestrictionRequest, error) {
	p := ext.NewPull(body, abkFlags)
	var r resortRestrictionRequest
	if _, err := p.Uint32(); err != nil { // reserved
		return r, err
	}
	hasStat, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasStat != 0 {
		if r.stat, err = pullStat(p); err != nil {
			return r, err
		}
	}
	hasInMids, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasInMids != 0 {
		tags, terr := p.PropTagsLong()
		if terr != nil {
			return r, terr
		}
		r.inmids = make([]uint32, len(tags))
		for i, t := range tags {
			r.inmids[i] = uint32(t)
		}
	}
	return r, skipAuxIn(p)
}

// encodeResortRestriction frames a ResortRestriction response: status + result +
// the updated STAT (always present) + the reordered MId array on success (else a
// single 0), then an empty AuxiliaryBuffer.
func (s *Server) encodeResortRestriction(result uint32, st stat, mids []uint32) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)      // status
	p.Uint32(result) // result
	p.Uint8(0xFF)    // STAT present (always)
	pushStat(p, st)
	if result != ecSuccess {
		p.Uint8(0)
	} else {
		p.Uint8(0xFF)
		_ = midArray(p, mids)
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}
