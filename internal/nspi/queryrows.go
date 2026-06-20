package nspi

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// defaultColumns is the column set QueryRows/QueryColumns use when the client
// requests none — the core address-book fields a GAL row supplies.
var defaultColumns = []mapi.PropTag{
	mapi.PrEntryID, mapi.PrDisplayName, mapi.PrAddrType,
	mapi.PrEmailAddress, mapi.PrSmtpAddress, mapi.PrObjectType, mapi.PrDisplayType,
}

// queryRowsRequest is the decoded QueryRows body ([MS-OXNSPI] 2.2.4 /
// [MS-OXCMAPIHTTP] 2.2.5.10): flags, an optional STAT, an explicit MId list
// (empty → walk the cursor), the max row count, and an optional column set.
type queryRowsRequest struct {
	stat     stat
	explicit []uint32
	count    uint32
	columns  []mapi.PropTag
}

func pullQueryRows(body []byte) (queryRowsRequest, error) {
	p := ext.NewPull(body, abkFlags)
	var r queryRowsRequest
	if _, err := p.Uint32(); err != nil { // flags (Ephemeral/Unicode; v1 emits permanent EIDs)
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
	tags, err := p.PropTagsLong() // explicit MId list (LPROPTAG_ARRAY shape)
	if err != nil {
		return r, err
	}
	r.explicit = make([]uint32, len(tags))
	for i, t := range tags {
		r.explicit[i] = uint32(t)
	}
	if r.count, err = p.Uint32(); err != nil {
		return r, err
	}
	hasCols, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasCols != 0 {
		if r.columns, err = p.PropTagsLong(); err != nil {
			return r, err
		}
	}
	return r, skipAuxIn(p)
}

// rowsetResult is the transport-neutral outcome of a STAT-cursor table query
// (QueryRows, SeekEntries): a result code, the updated cursor, and the
// column-projected row set. Both the MAPI/HTTP handler and the RPC/HTTP stub
// frame it with their own wire encoder.
type rowsetResult struct {
	result uint32
	stat   stat
	cols   []mapi.PropTag
	rows   []mapi.PropertyValues
}

// QueryRows handles the NSPI QueryRows request: it returns address-book rows
// either for an explicit MId list or by walking the STAT cursor forward.
func (s *Server) QueryRows(body []byte) []byte {
	req, err := pullQueryRows(body)
	if err != nil {
		return s.encodeQueryRows(ecError, stat{}, nil, nil)
	}
	r := s.queryRowsCore(req)
	return s.encodeQueryRows(r.result, r.stat, r.cols, r.rows)
}

// queryRowsCore runs the QueryRows semantics on a decoded request,
// transport-neutral: the MAPI/HTTP handler and the RPC/HTTP stub share it.
func (s *Server) queryRowsCore(req queryRowsRequest) rowsetResult {
	if req.count == 0 { // [MS-OXNSPI] 3.1.4.1.8: count must be non-zero
		return rowsetResult{result: ecInvalidParam, stat: req.stat}
	}
	if req.stat.codePage == cpWinUnicode {
		return rowsetResult{result: ecNotSupported, stat: req.stat}
	}
	cols := req.columns
	if len(cols) == 0 {
		cols = defaultColumns
	}
	if len(cols) > 100 {
		return rowsetResult{result: ecTableTooBig, stat: req.stat}
	}

	g := s.snapshot()
	st := req.stat
	var rows []mapi.PropertyValues
	if len(req.explicit) > 0 {
		// Explicit MId list: one row per id (an unknown id yields an all-error
		// row), and the cursor is not advanced.
		for _, mid := range req.explicit {
			if u, ok := g.byMID(mid); ok {
				rows = append(rows, galUserProps(u))
			} else {
				rows = append(rows, nil)
			}
		}
	} else {
		rows, st = g.viewFor(st.containerID).walk(st, req.count)
	}
	return rowsetResult{result: ecSuccess, stat: st, cols: cols, rows: rows}
}

// walk advances the cursor: it positions at STAT.cur_rec, applies the signed
// delta, fetches up to count rows, and returns the rows plus the updated STAT
// (cur_rec at the next row or END_OF_TABLE, num_pos/total_rec refreshed). It runs
// over whichever view the container selected (the GAL browse view, or a named
// list's type view), skipping the entries hidden on that surface while their MIds
// stay valid for a direct fetch.
func (v galView) walk(st stat, count uint32) ([]mapi.PropertyValues, stat) {
	total := v.total()
	start := v.position(st.curRec)
	if st.delta >= 0 {
		start += int(st.delta)
		if start > total {
			start = total
		}
	} else if uint32(-st.delta) > st.numPos {
		start = 0
	} else {
		start += int(st.delta)
	}
	if start < 0 {
		start = 0
	}
	n := min(total-start, int(count))
	var rows []mapi.PropertyValues
	for i := start; i < start+n; i++ {
		rows = append(rows, galUserProps(v.userAt(i)))
	}
	if start+n >= total {
		st.curRec = midEndOfTable
	} else {
		st.curRec = v.midAt(start + n)
	}
	st.delta = 0
	st.numPos = uint32(start + n)
	st.totalRec = uint32(total)
	return rows, st
}

// encodeQueryRows frames a QueryRows response: status + result + the updated
// STAT (always present) + the row set (columns + rows) on success, or a single 0
// in place of the rows on failure, then an empty AuxiliaryBuffer.
func (s *Server) encodeQueryRows(result uint32, st stat, cols []mapi.PropTag, rows []mapi.PropertyValues) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)      // status
	p.Uint32(result) // result
	p.Uint8(0xFF)    // STAT present
	pushStat(p, st)
	if result != ecSuccess {
		p.Uint8(0) // no row set
	} else {
		p.Uint8(0xFF)
		_ = pushColRow(p, cols, rows)
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}

// updateStatRequest is the decoded UpdateStat body ([MS-OXNSPI] 2.2.4): a
// reserved word, an optional STAT, and whether the client wants the applied
// delta reported back.
type updateStatRequest struct {
	stat           stat
	deltaRequested bool
}

func pullUpdateStat(body []byte) (updateStatRequest, error) {
	p := ext.NewPull(body, abkFlags)
	var r updateStatRequest
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
	dr, err := p.Uint8()
	if err != nil {
		return r, err
	}
	r.deltaRequested = dr != 0
	return r, skipAuxIn(p)
}

// updateStatResult is the transport-neutral outcome of UpdateStat: a result
// code, the repositioned cursor, and the applied row delta (reported only when
// the client requested it).
type updateStatResult struct {
	result   uint32
	stat     stat
	hasDelta bool
	delta    int32
}

// UpdateStat repositions the cursor by STAT.delta without returning rows,
// reporting the applied row delta when the client asked for it.
func (s *Server) UpdateStat(body []byte) []byte {
	req, err := pullUpdateStat(body)
	if err != nil {
		return s.encodeUpdateStat(ecError, stat{}, false, 0)
	}
	r := s.updateStatCore(req)
	return s.encodeUpdateStat(r.result, r.stat, r.hasDelta, r.delta)
}

// updateStatCore runs the UpdateStat semantics on a decoded request,
// transport-neutral: the MAPI/HTTP handler and the RPC/HTTP stub share it.
func (s *Server) updateStatCore(req updateStatRequest) updateStatResult {
	if req.stat.codePage == cpWinUnicode {
		return updateStatResult{result: ecNotSupported, stat: req.stat}
	}
	st := req.stat
	v := s.snapshot().viewFor(st.containerID)
	total := v.total()
	initRow := v.position(st.curRec)
	row := initRow
	if st.delta < 0 && uint32(-st.delta) >= uint32(row) {
		row = 0
	} else {
		row += int(st.delta)
	}
	if row >= total {
		row = total
		st.curRec = midEndOfTable
	} else {
		st.curRec = v.midAt(row)
	}
	delta := int32(row - initRow)
	st.delta = 0
	st.numPos = uint32(row)
	st.totalRec = uint32(total)
	return updateStatResult{result: ecSuccess, stat: st, hasDelta: req.deltaRequested, delta: delta}
}

// encodeUpdateStat frames an UpdateStat response: status + result + the updated
// STAT (always present) + the applied delta (present only when requested on a
// success), then an empty AuxiliaryBuffer.
func (s *Server) encodeUpdateStat(result uint32, st stat, hasDelta bool, delta int32) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)      // status
	p.Uint32(result) // result
	p.Uint8(0xFF)    // STAT present
	pushStat(p, st)
	if hasDelta && result == ecSuccess {
		p.Uint8(0xFF)
		p.Uint32(uint32(delta))
	} else {
		p.Uint8(0)
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}

// QueryColumns handles the NSPI QueryColumns request: it reports the column set
// the server can supply for address-book rows.
func (s *Server) QueryColumns(body []byte) []byte {
	p := ext.NewPull(body, abkFlags)
	if _, err := p.Uint32(); err != nil { // reserved
		return s.encodeQueryColumns(ecError, nil)
	}
	if _, err := p.Uint32(); err != nil { // flags
		return s.encodeQueryColumns(ecError, nil)
	}
	if err := skipAuxIn(p); err != nil {
		return s.encodeQueryColumns(ecError, nil)
	}
	return s.encodeQueryColumns(ecSuccess, s.queryColumnsCore())
}

// queryColumnsCore returns the fixed address-book column set every GAL row
// supplies; the NSPI result is always ecSuccess. Transport-neutral.
func (s *Server) queryColumnsCore() []mapi.PropTag {
	return defaultColumns
}

// encodeQueryColumns frames a QueryColumns response: status + result + the
// column proptag array on success (else a single 0), then an empty
// AuxiliaryBuffer.
func (s *Server) encodeQueryColumns(result uint32, cols []mapi.PropTag) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)      // status
	p.Uint32(result) // result
	if result != ecSuccess {
		p.Uint8(0)
	} else {
		p.Uint8(0xFF)
		_ = p.PropTagsLong(cols)
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}
