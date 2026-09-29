package rop

import (
	"errors"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// searchCriteriaRequest is a parsed RopSetSearchCriteria request ([MS-OXCROPS]
// 2.2.4.4.1). A nil restriction keeps the stored one and an empty scope keeps the
// stored scope.
type searchCriteriaRequest struct {
	restriction *mapi.Restriction
	scope       []mapi.EID
	flags       uint32
}

// pullSearchCriteriaRequest reads a RopSetSearchCriteria request: a 16-bit
// restriction size and the restriction, a 16-bit count of folder ids and the ids,
// and the search flags. framed is false when the bytes do not hold the request,
// which ends the batch; a restriction that is framed but does not parse is
// reported as ecError on a request that was still read in full.
func pullSearchCriteriaRequest(p *ext.Pull) (req searchCriteriaRequest, ec uint32, framed bool) {
	resSize, err := p.Uint16()
	if err != nil {
		return req, ecSuccess, false
	}
	raw, err := p.Raw(int(resSize))
	if err != nil {
		return req, ecSuccess, false
	}
	ids, e1 := p.Uint64ArrayShort()
	flags, e2 := p.Uint32()
	if e1 != nil || e2 != nil {
		return req, ecSuccess, false
	}
	req.flags = flags
	for _, id := range ids {
		req.scope = append(req.scope, mapi.EID(id))
	}
	if resSize == 0 {
		return req, ecSuccess, true
	}
	r, err := ext.NewPull(raw, ext.FlagUTF16).Restriction()
	if err != nil {
		return req, ecError, true
	}
	req.restriction = &r
	return req, ecSuccess, true
}

// ropSetSearchCriteria handles RopSetSearchCriteria ([MS-OXCFOLD] 2.2.1.4): it
// sets the restriction, the scope folders and the search flags of a search
// folder. A delegate needs owner rights on the search folder and read rights on
// every scope folder. A request that names neither a restriction nor a scope and
// does not restart the search changes nothing, as Exchange does.
func (s *Session) ropSetSearchCriteria(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	req, ec, framed := pullSearchCriteriaRequest(p)
	if !framed {
		return false
	}
	folder, ok := s.openFolder(out, ropSetSearchCriteria, handles, hindex, hindex)
	if !ok {
		return true
	}
	if ec == ecSuccess {
		ec = s.searchFolderError(folder)
	}
	if ec == ecSuccess && s.denyWrite(out, ropSetSearchCriteria, hindex, folder.store, folder.folderID, mapi.FrightsOwner) {
		return true
	}
	if ec == ecSuccess {
		ec = s.setSearchCriteria(folder, req)
	}
	if ec == ecAccessDenied {
		s.logAuthzDeny(ropSetSearchCriteria, folder.store, folder.folderID, mapi.FrightsReadAny)
	}
	writeErr(out, ropSetSearchCriteria, hindex, ec)
	return true
}

// searchFolderError is the return code for search criteria on folder: success for
// a search folder, ecNotSearchFolder for any other folder.
func (s *Session) searchFolderError(folder *object) uint32 {
	isSearch, err := folder.store.IsSearchFolder(folder.folderID)
	switch {
	case errors.Is(err, objectstore.ErrNotFound):
		return ecNotFound
	case err != nil:
		return ecError
	case !isSearch:
		return ecNotSearchFolder
	}
	return ecSuccess
}

// setSearchCriteria checks the request against the stored criteria and the
// caller's rights on each scope folder, then stores it.
func (s *Session) setSearchCriteria(folder *object, req searchCriteriaRequest) uint32 {
	if done, ec := partialSearchChange(folder, req); done {
		return ec
	}
	scope, ec := s.searchScope(folder.store, req.scope)
	if ec != ecSuccess {
		return ec
	}
	err := folder.store.SetSearchCriteria(folder.folderID, objectstore.SearchCriteria{
		Restriction: req.restriction, Scope: scope, Flags: req.flags,
	})
	switch {
	case err == nil:
		return ecSuccess
	case errors.Is(err, objectstore.ErrSearchScope):
		return ecSearchFolderScopeViolation
	case errors.Is(err, objectstore.ErrSearchNotInitialized):
		return ecNotInitialized
	}
	return ecError
}

// partialSearchChange answers a request that keeps the stored restriction or
// scope. It is done when the folder has no criteria to keep, or when the request
// names neither and does not restart the search, which changes nothing.
func partialSearchChange(folder *object, req searchCriteriaRequest) (done bool, ec uint32) {
	if req.restriction != nil && len(req.scope) > 0 {
		return false, ecSuccess
	}
	c, _, err := folder.store.GetSearchCriteria(folder.folderID)
	switch {
	case err != nil:
		return true, ecError
	case c.Restriction == nil:
		return true, ecNotInitialized
	case req.flags&mapi.SearchRestart == 0 && req.restriction == nil && len(req.scope) == 0:
		return true, ecSuccess
	}
	return false, ecSuccess
}

// searchScope turns the scope folder ids into store ids. A folder of another
// store is outside the search's reach, and a delegate may search only a folder
// they may read.
func (s *Session) searchScope(store *objectstore.Store, eids []mapi.EID) ([]int64, uint32) {
	caller, delegate := s.delegateCallers[store]
	scope := make([]int64, 0, len(eids))
	for _, eid := range eids {
		if eid.ReplID() != 1 {
			return nil, ecSearchFolderScopeViolation
		}
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		fid := int64(eid.GCValue())
		if delegate {
			rights, err := store.ResolvePermission(fid, caller)
			if err != nil {
				return nil, ecError
			}
			if rights&(mapi.FrightsOwner|mapi.FrightsReadAny) == 0 {
				return nil, ecAccessDenied
			}
		}
		scope = append(scope, fid)
	}
	return scope, ecSuccess
}

// ropGetSearchCriteria handles RopGetSearchCriteria ([MS-OXCFOLD] 2.2.1.5): it
// returns a search folder's restriction and scope folders, each when the request
// asks for it, and its search state. A client that does not use Unicode gets the
// restriction's string properties as 8-bit strings.
func (s *Session) ropGetSearchCriteria(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	head, err := p.Raw(3) // UseUnicode, IncludeRestriction, IncludeFolders
	if err != nil {
		return false
	}
	folder, ok := s.openFolder(out, ropGetSearchCriteria, handles, hindex, hindex)
	if !ok {
		return true
	}
	if ec := s.searchFolderError(folder); ec != ecSuccess {
		writeErr(out, ropGetSearchCriteria, hindex, ec)
		return true
	}
	c, state, err := folder.store.GetSearchCriteria(folder.folderID)
	if err != nil {
		writeErr(out, ropGetSearchCriteria, hindex, ecError)
		return true
	}
	resBytes, scope, err := searchCriteriaReply(c, head[0] != 0, head[1] != 0, head[2] != 0)
	if err != nil {
		writeErr(out, ropGetSearchCriteria, hindex, ecError)
		return true
	}
	out.Uint8(ropGetSearchCriteria)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(uint16(len(resBytes))) // #nosec G115 -- restrictionBytes bounds it to 16 bits
	out.Raw(resBytes)
	out.Uint8(s.logonID)
	if err := out.Uint64ArrayShort(scope); err != nil {
		return false
	}
	out.Uint32(state)
	return true
}

// searchCriteriaReply shapes stored criteria as RopGetSearchCriteria returns
// them: the serialized restriction and the scope folder ids, each only when the
// request asks for it, with 8-bit strings for a client that does not use Unicode.
func searchCriteriaReply(c objectstore.SearchCriteria, unicode, withRestriction, withFolders bool) ([]byte, []uint64, error) {
	var res []byte
	if withRestriction && c.Restriction != nil {
		r := *c.Restriction
		if !unicode {
			r = restrictionToString8(r)
		}
		var err error
		if res, err = restrictionBytes(r); err != nil {
			return nil, nil, err
		}
	}
	var scope []uint64
	if withFolders {
		for _, fid := range c.Scope {
			scope = append(scope, uint64(mapi.MakeEIDEx(1, uint64(fid)))) // #nosec G115 -- a folder id is never negative
		}
	}
	return res, scope, nil
}

// restrictionBytes serializes a restriction as a ROP carries it. A restriction
// past the 16-bit size field is an error.
func restrictionBytes(r mapi.Restriction) ([]byte, error) {
	b := ext.NewPush(ext.FlagUTF16)
	if err := b.Restriction(r); err != nil {
		return nil, err
	}
	if len(b.Bytes()) > 0xFFFF {
		return nil, ext.ErrFormat
	}
	return b.Bytes(), nil
}

// restrictionToString8 returns r with every Unicode string property tag, and the
// values it compares, retyped as 8-bit strings.
func restrictionToString8(r mapi.Restriction) mapi.Restriction {
	switch v := r.Value.(type) {
	case []mapi.Restriction:
		kids := make([]mapi.Restriction, len(v))
		for i, k := range v {
			kids[i] = restrictionToString8(k)
		}
		r.Value = kids
	case mapi.Restriction:
		r.Value = restrictionToString8(v)
	case mapi.SubRestriction:
		v.Res = restrictionToString8(v.Res)
		r.Value = v
	case mapi.CountRestriction:
		v.SubRes = restrictionToString8(v.SubRes)
		r.Value = v
	case mapi.CommentRestriction:
		r.Value = commentToString8(v)
	default:
		r.Value = leafToString8(r.Value)
	}
	return r
}

// commentToString8 retypes a comment node's values and its nested restriction.
func commentToString8(c mapi.CommentRestriction) mapi.CommentRestriction {
	vals := make([]mapi.TaggedPropVal, len(c.PropVals))
	for i, pv := range c.PropVals {
		vals[i] = mapi.TaggedPropVal{Tag: string8Tag(pv.Tag), Value: pv.Value}
	}
	c.PropVals = vals
	if c.Res != nil {
		inner := restrictionToString8(*c.Res)
		c.Res = &inner
	}
	return c
}

// leafToString8 retypes the tags of a restriction node that has no children.
func leafToString8(v any) any {
	switch n := v.(type) {
	case mapi.ContentRestriction:
		n.PropTag, n.PropVal.Tag = string8Tag(n.PropTag), string8Tag(n.PropVal.Tag)
		return n
	case mapi.PropertyRestriction:
		n.PropTag, n.PropVal.Tag = string8Tag(n.PropTag), string8Tag(n.PropVal.Tag)
		return n
	case mapi.ComparePropsRestriction:
		n.PropTag1, n.PropTag2 = string8Tag(n.PropTag1), string8Tag(n.PropTag2)
		return n
	case mapi.SizeRestriction:
		n.PropTag = string8Tag(n.PropTag)
		return n
	case mapi.ExistRestriction:
		n.PropTag = string8Tag(n.PropTag)
		return n
	}
	return v
}

// string8Tag retypes a Unicode string tag, single or multivalue, as the 8-bit
// string tag of the same property. Any other tag is returned unchanged.
func string8Tag(t mapi.PropTag) mapi.PropTag {
	multi := t.Type() & mapi.MviFlag
	if t.Type()&^mapi.MviFlag != mapi.PtUnicode {
		return t
	}
	return t.WithType(multi | mapi.PtString8)
}
