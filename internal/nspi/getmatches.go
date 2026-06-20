package nspi

import (
	"strings"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// Address-book sort types ([MS-OXNSPI] 2.3.1.1). GetMatches supports only
// display-name ordering; any other sort_type is rejected.
const (
	sortTypeDisplayName         uint32 = 0x0
	sortTypePhoneticDisplayName uint32 = 0x3
	sortTypeDisplayNameRO       uint32 = 0x3E8
	sortTypeDisplayNameW        uint32 = 0x3E9
)

func isDisplayNameSort(t uint32) bool {
	switch t {
	case sortTypeDisplayName, sortTypePhoneticDisplayName, sortTypeDisplayNameRO, sortTypeDisplayNameW:
		return true
	}
	return false
}

// getMatchesRequest is the decoded GetMatches body ([MS-OXNSPI] 2.2.4 /
// [MS-OXCMAPIHTTP] 2.2.5.6). reserved1 and a present property name both force
// ecNotSupported; the explicit input-MId list is parsed off the wire but not
// acted on (the MAPI/HTTP interface drives matching from the filter alone).
type getMatchesRequest struct {
	reserved1   uint32
	stat        stat
	filter      *mapi.Restriction
	hasPropName bool
	rowCount    uint32
	columns     []mapi.PropTag
	hasCols     bool
}

func pullGetMatches(body []byte) (getMatchesRequest, error) {
	p := ext.NewPull(body, abkFlags)
	var r getMatchesRequest
	var err error
	if r.reserved1, err = p.Uint32(); err != nil {
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
	// Explicit input-MId list: consumed and discarded (the interface handler
	// takes no inmids — matching is driven by the filter only).
	hasInMids, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasInMids != 0 {
		if _, err = p.PropTagsLong(); err != nil {
			return r, err
		}
	}
	if _, err = p.Uint32(); err != nil { // reserved
		return r, err
	}
	hasFilter, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasFilter != 0 {
		f, ferr := p.Restriction()
		if ferr != nil {
			return r, ferr
		}
		r.filter = &f
	}
	hasPropName, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasPropName != 0 {
		// A present property name is unsupported; the remaining fields are
		// unused once we reject, so do not parse the name body.
		r.hasPropName = true
		return r, nil
	}
	if r.rowCount, err = p.Uint32(); err != nil {
		return r, err
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

// GetMatches handles the NSPI GetMatches request ([MS-OXNSPI] 2.2.4): it
// returns the MIds of GAL entries satisfying a restriction, capped at the
// requested row count, plus a column-projected row per match. Outlook uses it
// with a PR_ANR restriction as an alternative to ResolveNamesW, so ANR matching
// shares that predicate.
func (s *Server) GetMatches(body []byte) []byte {
	req, err := pullGetMatches(body)
	if err != nil {
		return s.encodeGetMatches(ecError, stat{}, nil, nil, nil)
	}
	r := s.getMatchesCore(req)
	return s.encodeGetMatches(r.result, r.stat, r.mids, r.cols, r.rows)
}

// getMatchesResult is the transport-neutral outcome of GetMatches: a result
// code, the updated cursor, the matched MId list, and the column-projected row
// set.
type getMatchesResult struct {
	result uint32
	stat   stat
	mids   []uint32
	cols   []mapi.PropTag
	rows   []mapi.PropertyValues
}

// getMatchesCore runs the GetMatches semantics on a decoded request,
// transport-neutral: the MAPI/HTTP handler and the RPC/HTTP stub share it.
func (s *Server) getMatchesCore(req getMatchesRequest) getMatchesResult {
	st := req.stat
	if st.codePage == cpWinUnicode {
		return getMatchesResult{result: ecNotSupported, stat: st}
	}
	if !isDisplayNameSort(st.sortType) {
		return getMatchesResult{result: ecNotSupported, stat: st}
	}
	if req.reserved1 != 0 || req.hasPropName {
		return getMatchesResult{result: ecNotSupported, stat: st}
	}
	cols := req.columns
	if !req.hasCols || len(cols) == 0 {
		cols = defaultColumns
	}
	if len(cols) > 100 {
		return getMatchesResult{result: ecTableTooBig, stat: st}
	}

	g := s.snapshot()
	var mids []uint32
	switch {
	case st.containerID == uint32(mapi.PrEmsAbMember):
		// Expand the distribution list at cur_rec into its members ([MS-OXNSPI]
		// 3.1.4.1.10): the client selects the PR_EMS_AB_MEMBER container to read a
		// list's membership. Members hidden from address lists are dropped.
		if exp, ok := s.gal.(mlistExpander); ok {
			mids = g.memberMIDs(st.curRec, exp, int(req.rowCount))
		}
	case st.containerID == uint32(mapi.PrEmsAbPublicDelegates):
		// Read the public-delegate list of the mailbox at cur_rec ([MS-OXNSPI]
		// 3.1.4.1.10): delegates hidden from the delegate list, and any the filter
		// excludes, are dropped. Public delegates are world-readable, so this takes
		// no caller identity.
		if reader, ok := s.gal.(delegateReader); ok {
			mids = g.delegateMIDs(st.curRec, reader, req.filter, int(req.rowCount))
		}
	case st.containerID == uint32(galContainerID):
		// The GAL honors both the GAL-browse and the name-resolution hide bits.
		mids = g.matchAll(req.filter, req.rowCount, st, abHideFromGAL|abHideResolve, nil)
	default:
		// A named address list: restrict to its recipient type and honor the
		// address-list and name-resolution hide bits. An unknown container matches
		// nothing.
		if al, ok := addressListByID(int32(st.containerID)); ok {
			mids = g.matchAll(req.filter, req.rowCount, st, abHideFromAL|abHideResolve, &al)
		}
	}
	rows := make([]mapi.PropertyValues, len(mids))
	for i, mid := range mids {
		if u, ok := g.byMID(mid); ok {
			rows[i] = galUserProps(u)
		}
		// else: an unresolvable MId leaves a nil row, which serializes as an
		// all-error PROPERTY_ROW (matching the reference's ptyperror fallback).
	}
	// [MS-OXNSPI] 3.1.4.1.10 point 16: the container bookmark becomes cur_rec.
	st.containerID = st.curRec
	return getMatchesResult{result: ecSuccess, stat: st, mids: mids, cols: cols, rows: rows}
}

// matchAll returns the MIds of a container's entries satisfying the filter,
// walking from the STAT position and capped at rowCount. A nil filter matches the
// single entry at cur_rec (the reference's no-filter branch); otherwise every
// eligible entry from the position onward is tested via matchNode. An entry is
// eligible when none of hideMask's bits are set and, for a named list, its
// recipient display type matches the list (list is nil for the GAL).
func (g gal) matchAll(filter *mapi.Restriction, rowCount uint32, st stat, hideMask uint32, list *addressList) []uint32 {
	if filter == nil {
		if u, ok := g.resolveEntry(st.curRec); ok {
			return []uint32{u.mid}
		}
		return nil
	}
	var mids []uint32
	for i := g.position(st.curRec); i < len(g.users); i++ {
		if uint32(len(mids)) >= rowCount {
			break
		}
		u := g.users[i]
		if u.hidden&hideMask != 0 {
			continue
		}
		if list != nil && u.dispType != list.dispType {
			continue
		}
		if matchNode(u, filter) {
			mids = append(mids, u.mid)
		}
	}
	return mids
}

// matchNode evaluates an NSPI search restriction against a GAL user, mirroring
// the reference address-book match: AND/OR/NOT recurse; a PR_ANR property
// restriction matches via the shared display/SMTP-substring predicate; a
// property restriction on a stored address-book tag compares the user's value
// with the requested relational operator; an EXIST restriction tests presence.
// Content and the structural restriction kinds the reference rejects for
// address-book filtering do not match.
func matchNode(u galUser, res *mapi.Restriction) bool {
	switch res.Type {
	case mapi.ResAnd:
		kids, _ := res.Value.([]mapi.Restriction)
		for i := range kids {
			if !matchNode(u, &kids[i]) {
				return false
			}
		}
		return true
	case mapi.ResOr:
		kids, _ := res.Value.([]mapi.Restriction)
		for i := range kids {
			if matchNode(u, &kids[i]) {
				return true
			}
		}
		return false
	case mapi.ResNot:
		inner, ok := res.Value.(mapi.Restriction)
		if !ok {
			return false
		}
		return !matchNode(u, &inner)
	case mapi.ResProperty:
		pr, ok := res.Value.(mapi.PropertyRestriction)
		if !ok {
			return false
		}
		return matchProperty(u, pr)
	case mapi.ResExist:
		ex, ok := res.Value.(mapi.ExistRestriction)
		if !ok {
			return false
		}
		_, present := galUserProps(u).Get(ex.PropTag)
		return present
	default:
		// RES_CONTENT and the structural kinds are unevaluated for the GAL.
		return false
	}
}

// matchProperty evaluates a single property restriction. PR_ANR (and its ANSI
// variant) is a search directive — not a stored value — so it matches the token
// against the entry's display name and SMTP address, the same predicate
// ResolveNamesW uses. Any other tag is compared against the entry's value.
func matchProperty(u galUser, pr mapi.PropertyRestriction) bool {
	if pr.PropTag == mapi.PrAnr || pr.PropTag == mapi.PrAnrA {
		token, _ := pr.PropVal.Value.(string)
		token = stripTypePrefix(token)
		return token != "" && u.matchesToken(token)
	}
	got, ok := galUserProps(u).Get(pr.PropTag)
	if !ok {
		return false
	}
	cmp, ok := compareProp(pr.PropTag, got, pr.PropVal.Value)
	if !ok {
		return false
	}
	return threeWayEval(pr.Relop, cmp)
}

// threeWayEval applies a relational operator to a three-way comparison result
// (cmp < 0, == 0, or > 0).
func threeWayEval(relop mapi.Relop, cmp int) bool {
	switch relop {
	case mapi.RelopLT:
		return cmp < 0
	case mapi.RelopLE:
		return cmp <= 0
	case mapi.RelopGT:
		return cmp > 0
	case mapi.RelopGE:
		return cmp >= 0
	case mapi.RelopEQ:
		return cmp == 0
	case mapi.RelopNE:
		return cmp != 0
	}
	return false
}

// compareProp three-way compares a stored value against a wanted value, typed by
// the proptag. ok is false for types the address-book filter does not compare
// (the reference handles short, long, boolean, and string).
func compareProp(tag mapi.PropTag, got, want any) (cmp int, ok bool) {
	switch tag.Type() {
	case mapi.PtShort, mapi.PtLong:
		a, ok1 := toInt64(got)
		b, ok2 := toInt64(want)
		if !ok1 || !ok2 {
			return 0, false
		}
		switch {
		case a < b:
			return -1, true
		case a > b:
			return 1, true
		default:
			return 0, true
		}
	case mapi.PtBoolean:
		a, ok1 := got.(bool)
		b, ok2 := want.(bool)
		if !ok1 || !ok2 {
			return 0, false
		}
		switch {
		case a == b:
			return 0, true
		case !a:
			return -1, true
		default:
			return 1, true
		}
	case mapi.PtString8, mapi.PtUnicode:
		a, ok1 := got.(string)
		b, ok2 := want.(string)
		if !ok1 || !ok2 {
			return 0, false
		}
		return strings.Compare(strings.ToLower(a), strings.ToLower(b)), true
	}
	return 0, false
}

// toInt64 widens the integer property types compareProp handles.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

// encodeGetMatches frames a GetMatches response: status + result + the updated
// STAT (always present) + on success the matched MID_ARRAY and the projected
// row set, or a pair of 0 markers on failure, then an empty AuxiliaryBuffer.
func (s *Server) encodeGetMatches(result uint32, st stat, mids []uint32, cols []mapi.PropTag, rows []mapi.PropertyValues) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)      // status
	p.Uint32(result) // result
	p.Uint8(0xFF)    // STAT present (always)
	pushStat(p, st)
	if result != ecSuccess {
		p.Uint8(0) // MIds absent
		p.Uint8(0) // row set absent
	} else {
		p.Uint8(0xFF)
		_ = midArray(p, mids)
		p.Uint8(0xFF)
		_ = pushColRow(p, cols, rows)
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}
