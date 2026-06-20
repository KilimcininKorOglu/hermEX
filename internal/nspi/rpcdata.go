package nspi

import (
	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// galTableVersion is the version reported in the GetSpecialTable OUT. The v1
// address-book hierarchy is a single static GAL container, so the version never
// changes and the client caches the hierarchy after one read.
const galTableVersion uint32 = 1

// --- shared NDR IN/OUT helpers for the data opnums ---

// pullHandle consumes the leading NSPI context handle (20 bytes) every data op
// carries. The GAL keeps no handle table, so the value is discarded; only its
// well-formedness is checked.
func pullHandle(p *ndr.Pull) error {
	_, _, err := pullCtxHandleNDR(p)
	return err
}

// pullPtrProptags reads a unique-pointer proptag array: the referent, then (if
// non-null) the N+1-framed array body. A null pointer yields a nil slice, which
// the cores read as "column set absent".
func pullPtrProptags(p *ndr.Pull) ([]mapi.PropTag, error) {
	ref, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if ref == 0 {
		return nil, nil
	}
	vals, err := pullU32ArrayNDR(p)
	if err != nil {
		return nil, err
	}
	tags := make([]mapi.PropTag, len(vals))
	for i, v := range vals {
		tags[i] = mapi.PropTag(v)
	}
	return tags, nil
}

// pullPtrMIDs reads a unique-pointer MID array (the same N+1-framed body as a
// proptag array, MID semantics). A null pointer yields a nil slice.
func pullPtrMIDs(p *ndr.Pull) ([]uint32, error) {
	ref, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if ref == 0 {
		return nil, nil
	}
	return pullU32ArrayNDR(p)
}

// pushPtrProptags writes a unique-pointer proptag array: present emits the
// referent then the N+1-framed body; absent emits a NULL referent.
func pushPtrProptags(p *ndr.Push, tags []mapi.PropTag, present bool) {
	p.UniquePtr(present)
	if present {
		pushProptagsNDR(p, tags)
	}
}

// pushPtrMIDs writes a unique-pointer MID array (the u32-array shape).
func pushPtrMIDs(p *ndr.Push, mids []uint32, present bool) {
	p.UniquePtr(present)
	if present {
		pushU32ArrayNDR(p, mids)
	}
}

// rowTags returns a row's own proptags so a pre-built row (GetProps,
// GetSpecialTable) re-serializes verbatim through the projecting rowset helpers:
// projecting a row against its own tags is the identity.
func rowTags(row mapi.PropertyValues) []mapi.PropTag {
	tags := make([]mapi.PropTag, len(row))
	for i, v := range row {
		tags[i] = v.Tag
	}
	return tags
}

// encodeRowsetNDR frames the OUT shared by QueryRows and SeekEntries: the updated
// STAT, a unique-pointer PROPROW_SET (NULL on a non-success result), then the
// result. An encode error (an unsupported value type) is reported as an NDR fault
// rather than a truncated response.
func encodeRowsetNDR(r rowsetResult) ([]byte, uint32) {
	out := ndr.NewPush()
	pushStatNDR(out, r.stat)
	ok := r.result == ecSuccess
	out.UniquePtr(ok)
	if ok {
		if err := pushRowSetNDR(out, r.cols, r.rows); err != nil {
			return nil, ndr.FaultNdr
		}
	}
	out.Uint32(r.result)
	return out.Bytes(), 0
}

// --- STAT-cursor table ops ---

// rpcUpdateStat handles NspiUpdateStat (opnum 2): handle, reserved, STAT, and an
// [in,out] unique-pointer delta whose presence asks for the applied row delta
// back. OUT: the repositioned STAT, the delta (present only when requested), then
// the result.
func (s *Server) rpcUpdateStat(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	if _, err := p.Uint32(); err != nil { // reserved
		return nil, ndr.FaultNdr
	}
	st, err := pullStatNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	ref, err := p.Uint32() // delta [in,out] referent
	if err != nil {
		return nil, ndr.FaultNdr
	}
	deltaRequested := ref != 0
	if deltaRequested {
		if _, err := p.Int32(); err != nil { // the in delta value, ignored
			return nil, ndr.FaultNdr
		}
	}
	r := s.updateStatCore(updateStatRequest{stat: st, deltaRequested: deltaRequested})
	out := ndr.NewPush()
	pushStatNDR(out, r.stat)
	out.UniquePtr(r.hasDelta)
	if r.hasDelta {
		out.Int32(r.delta)
	}
	out.Uint32(r.result)
	return out.Bytes(), 0
}

// rpcQueryRows handles NspiQueryRows (opnum 3): handle, flags, STAT, the inline
// explicit-MID array, the requested row count, and a unique-pointer column set.
// OUT: STAT + a unique-pointer PROPROW_SET + the result.
func (s *Server) rpcQueryRows(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	if _, err := p.Uint32(); err != nil { // flags
		return nil, ndr.FaultNdr
	}
	st, err := pullStatNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	explicit, err := pullInlineMIDArrayNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	count, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	cols, err := pullPtrProptags(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	r := s.queryRowsCore(queryRowsRequest{stat: st, explicit: explicit, count: count, columns: cols})
	return encodeRowsetNDR(r)
}

// rpcSeekEntries handles NspiSeekEntries (opnum 4): handle, reserved, STAT, the
// inline target PROPERTY_VALUE, a unique-pointer MID table to seek within, and a
// unique-pointer column set. OUT: STAT + a unique-pointer PROPROW_SET + result.
func (s *Server) rpcSeekEntries(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	reserved, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	st, err := pullStatNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	target, err := pullPropValNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	table, err := pullPtrMIDs(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	cols, err := pullPtrProptags(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	r := s.seekEntriesCore(seekEntriesRequest{
		reserved: reserved, stat: st, target: target,
		table: table, columns: cols, hasCols: cols != nil,
	})
	return encodeRowsetNDR(r)
}

// rpcCompareMids handles NspiCompareMIds (opnum 10): handle, reserved, STAT, and
// the two MIds. OUT: the signed comparison (no leading referent) then the result.
func (s *Server) rpcCompareMids(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	if _, err := p.Uint32(); err != nil { // reserved
		return nil, ndr.FaultNdr
	}
	st, err := pullStatNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	mid1, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	mid2, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	r := s.compareMidsCore(compareMidsRequest{stat: st, mid1: mid1, mid2: mid2})
	out := ndr.NewPush()
	out.Int32(r.cmp)
	out.Uint32(r.result)
	return out.Bytes(), 0
}

// rpcResortRestriction handles NspiResortRestriction (opnum 6): handle, reserved,
// STAT, the inline MID array to reorder, and a reserved unique-pointer output-MID
// array (consumed and discarded). OUT: STAT + a unique-pointer reordered MID
// array + result.
func (s *Server) rpcResortRestriction(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	if _, err := p.Uint32(); err != nil { // reserved
		return nil, ndr.FaultNdr
	}
	st, err := pullStatNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	inmids, err := pullU32ArrayNDR(p) // inline (not pointer-wrapped)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	if _, err := pullPtrMIDs(p); err != nil { // reserved outmids, discarded
		return nil, ndr.FaultNdr
	}
	r := s.resortRestrictionCore(resortRestrictionRequest{stat: st, inmids: inmids})
	out := ndr.NewPush()
	pushStatNDR(out, r.stat)
	ok := r.result == ecSuccess
	pushPtrMIDs(out, r.mids, ok)
	out.Uint32(r.result)
	return out.Bytes(), 0
}

// --- lookup ops (no STAT in the OUT) ---

// rpcGetProps handles NspiGetProps (opnum 9): handle, flags, STAT, and a
// unique-pointer column set. OUT: a unique-pointer single PROPERTY_ROW (NULL only
// on an error that is neither success nor warn-with-errors) then the result. The
// row is already projected, so it serializes verbatim.
func (s *Server) rpcGetProps(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	if _, err := p.Uint32(); err != nil { // flags
		return nil, ndr.FaultNdr
	}
	st, err := pullStatNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	tags, err := pullPtrProptags(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	r := s.getPropsCore(getPropsRequest{stat: st, proptags: tags, hasTags: tags != nil})
	out := ndr.NewPush()
	ok := r.result == ecSuccess || r.result == ecWarnWithErrors
	out.UniquePtr(ok)
	if ok {
		if err := pushPropertyRowNDR(out, rowTags(r.row), r.row); err != nil {
			return nil, ndr.FaultNdr
		}
	}
	out.Uint32(r.result)
	return out.Bytes(), 0
}

// rpcGetPropList handles NspiGetPropList (opnum 8): handle, flags, the MId, and
// the code page. OUT: a unique-pointer proptag array (NULL on a non-success
// result) then the result.
func (s *Server) rpcGetPropList(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	flags, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	mid, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	codePage, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	r := s.getPropListCore(getPropListRequest{flags: flags, mid: mid, codePage: codePage})
	out := ndr.NewPush()
	pushPtrProptags(out, r.tags, r.result == ecSuccess)
	out.Uint32(r.result)
	return out.Bytes(), 0
}

// rpcQueryColumns handles NspiQueryColumns (opnum 16): handle, reserved, flags.
// OUT: a unique-pointer proptag array (always present — the result is always
// ecSuccess) then the result.
func (s *Server) rpcQueryColumns(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	if _, err := p.Uint32(); err != nil { // reserved
		return nil, ndr.FaultNdr
	}
	if _, err := p.Uint32(); err != nil { // flags
		return nil, ndr.FaultNdr
	}
	out := ndr.NewPush()
	pushPtrProptags(out, s.queryColumnsCore(), true)
	out.Uint32(ecSuccess)
	return out.Bytes(), 0
}

// rpcGetSpecialTable handles NspiGetSpecialTable (opnum 12): handle, flags, STAT,
// the client's cached table version. OUT: the table version FIRST, then a
// unique-pointer PROPROW_SET of container rows, then the result.
func (s *Server) rpcGetSpecialTable(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	flags, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	st, err := pullStatNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	if _, err := p.Uint32(); err != nil { // the client's cached version, ignored
		return nil, ndr.FaultNdr
	}
	r := s.getSpecialTableCore(getSpecialTableRequest{flags: flags, stat: st})
	out := ndr.NewPush()
	out.Uint32(galTableVersion)
	ok := r.result == ecSuccess && len(r.rows) > 0
	out.UniquePtr(ok)
	if ok {
		if err := pushRowSetNDR(out, rowTags(r.rows[0]), r.rows); err != nil {
			return nil, ndr.FaultNdr
		}
	}
	out.Uint32(r.result)
	return out.Bytes(), 0
}

// --- write-range ops: the GAL is read-only, so each decodes the handle and answers
// with a faithful MAPI error instead of an op-range fault. NSPI dispatches one opnum
// per stub, so the request body past what a handler reads is left unconsumed safely:
// there is no successor op to misalign. ---

// rpcModProps handles NspiModProps (opnum 11): nothing in the GAL is writable, so it
// returns ecNotSupported unconditionally — the exact reference behavior. The body past
// the handle (STAT, the delete-proptag array, the value row) is not decoded. OUT: the
// bare result.
func (s *Server) rpcModProps(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	out := ndr.NewPush()
	out.Uint32(ecNotSupported)
	return out.Bytes(), 0
}

// rpcGetTemplateInfo handles NspiGetTemplateInfo (opnum 13). hermEX ships no
// display-table (.abkt) archive, so a well-formed template request is permanently "no
// template for this locale" → ecUnknownLcid; a request that is not TI_TEMPLATE alone
// (e.g. one carrying TI_SCRIPT) is ecNotSupported, mirroring the reference's flags
// rung. The handle and Flags are read; Type/DN/CodePage/LocaleID are not — the answer
// never depends on them. OUT: a null pData unique-ptr then the result.
func (s *Server) rpcGetTemplateInfo(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	flags, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	result := ecUnknownLcid
	if flags&(tiTemplate|tiScript) != tiTemplate {
		result = ecNotSupported
	}
	out := ndr.NewPush()
	out.UniquePtr(false) // pData: no template row
	out.Uint32(result)
	return out.Bytes(), 0
}

// rpcModLinkAtt handles NspiModLinkAtt (opnum 14) over RPC/HTTP: editing the
// public-delegates list needs the caller's identity for the owner-only access
// check, which the RPC/HTTP dispatcher does not thread through, so this returns a
// blanket ecNotSupported. The op is served over MAPI/HTTP — the transport modern
// Outlook uses — by ModLinkAtt. Only the handle is decoded. OUT: the bare result.
func (s *Server) rpcModLinkAtt(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	out := ndr.NewPush()
	out.Uint32(ecNotSupported)
	return out.Bytes(), 0
}

// --- resolve / match ops ---

// rpcDNToMid handles NspiDNToMId (opnum 7): handle, reserved, and an 8-bit
// strings array of distinguished names. OUT: a unique-pointer MID array (always
// present — the result is always ecSuccess) then the result.
func (s *Server) rpcDNToMid(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	if _, err := p.Uint32(); err != nil { // reserved
		return nil, ndr.FaultNdr
	}
	names, err := pullStringsArrayNDR(p, false)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	out := ndr.NewPush()
	pushPtrMIDs(out, s.dnToMidCore(names), true)
	out.Uint32(ecSuccess)
	return out.Bytes(), 0
}

// rpcGetMatches handles NspiGetMatches (opnum 5): handle, reserved1, STAT, a
// reserved unique-pointer MID array (discarded), a reserved word, the
// unique-pointer restriction, a unique-pointer property name, the requested row
// count, and a unique-pointer column set. A present property name is unsupported,
// so — like the MAPI/HTTP handler — the core rejects on it without parsing the
// remaining fields. OUT: STAT + a unique-pointer MID array + a unique-pointer
// PROPROW_SET + result (two NULL referents on a non-success result).
func (s *Server) rpcGetMatches(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	reserved1, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	st, err := pullStatNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	if _, err := pullPtrMIDs(p); err != nil { // reserved input-MID list, discarded
		return nil, ndr.FaultNdr
	}
	if _, err := p.Uint32(); err != nil { // reserved
		return nil, ndr.FaultNdr
	}
	req := getMatchesRequest{reserved1: reserved1, stat: st}
	fref, err := p.Uint32() // restriction referent
	if err != nil {
		return nil, ndr.FaultNdr
	}
	if fref != 0 {
		f, ferr := pullRestrictionNDR(p)
		if ferr != nil {
			return nil, ndr.FaultNdr
		}
		req.filter = &f
	}
	pnref, err := p.Uint32() // property-name referent
	if err != nil {
		return nil, ndr.FaultNdr
	}
	if pnref != 0 {
		// A present property name is unsupported; reject without parsing its body
		// or the trailing fields, exactly as the MAPI/HTTP handler does.
		req.hasPropName = true
		return s.encodeGetMatchesNDR(s.getMatchesCore(req))
	}
	requested, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	req.rowCount = requested
	cols, err := pullPtrProptags(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	req.columns = cols
	req.hasCols = cols != nil
	return s.encodeGetMatchesNDR(s.getMatchesCore(req))
}

// encodeGetMatchesNDR frames the GetMatches OUT: STAT, a unique-pointer matched
// MID array, a unique-pointer PROPROW_SET, then the result (a non-success result
// emits a NULL referent for both the MID array and the row set).
func (s *Server) encodeGetMatchesNDR(r getMatchesResult) ([]byte, uint32) {
	out := ndr.NewPush()
	pushStatNDR(out, r.stat)
	ok := r.result == ecSuccess
	pushPtrMIDs(out, r.mids, ok)
	out.UniquePtr(ok)
	if ok {
		if err := pushRowSetNDR(out, r.cols, r.rows); err != nil {
			return nil, ndr.FaultNdr
		}
	}
	out.Uint32(r.result)
	return out.Bytes(), 0
}

// rpcResolveNames handles NspiResolveNames (opnum 19) and NspiResolveNamesW
// (opnum 20): handle, reserved, STAT, a unique-pointer column set, and the names
// array (8-bit for 19, UTF-16 for 20 — wide selects which). OUT: a unique-pointer
// per-name MID array + a unique-pointer PROPROW_SET + result (two NULL referents
// on a non-success result). The raw NSPI OUT carries no echoed code page (that is
// a MAPI/HTTP-only field), so the core's code page is dropped here.
func (s *Server) rpcResolveNames(stub []byte, wide bool) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if err := pullHandle(p); err != nil {
		return nil, ndr.FaultNdr
	}
	if _, err := p.Uint32(); err != nil { // reserved
		return nil, ndr.FaultNdr
	}
	st, err := pullStatNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	cols, err := pullPtrProptags(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	names, err := pullStringsArrayNDR(p, wide)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	r := s.resolveNamesCore(resolveNamesRequest{stat: st, columns: cols, names: names})
	out := ndr.NewPush()
	ok := r.result == ecSuccess
	pushPtrMIDs(out, r.mids, ok)
	out.UniquePtr(ok)
	if ok {
		if err := pushRowSetNDR(out, r.cols, r.rows); err != nil {
			return nil, ndr.FaultNdr
		}
	}
	out.Uint32(r.result)
	return out.Bytes(), 0
}
