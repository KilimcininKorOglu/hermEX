package nspi

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// getPropsRequest is the decoded GetProps body ([MS-OXNSPI] 2.2.4 /
// [MS-OXCMAPIHTTP] 2.2.5.4): flags, an optional STAT (whose cur_rec selects the
// entry and whose code page is echoed), and an optional explicit column set.
// hasTags distinguishes an absent column set (return the default property bag)
// from a present-but-empty one.
type getPropsRequest struct {
	stat     stat
	proptags []mapi.PropTag
	hasTags  bool
}

func pullGetProps(body []byte) (getPropsRequest, error) {
	p := ext.NewPull(body, abkFlags)
	var r getPropsRequest
	if _, err := p.Uint32(); err != nil { // flags (fEphID; v1 emits permanent EIDs)
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
	hasTags, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasTags != 0 {
		r.hasTags = true
		if r.proptags, err = p.PropTagsLong(); err != nil {
			return r, err
		}
	}
	return r, skipAuxIn(p)
}

// GetProps handles the NSPI GetProps request ([MS-OXNSPI] 2.2.4): it returns the
// property values of the single entry addressed by STAT.cur_rec. With an
// explicit column set, a requested-but-absent property is returned as a
// PT_ERROR(ecNotFound) marker and the result is ecWarnWithErrors; with no column
// set, the entry's full default bag is returned. An unknown entry yields a row
// of PT_ERROR markers ([MS-OXNSPI] 3.1.4.1.7 point 11).
func (s *Server) GetProps(body []byte) []byte {
	req, err := pullGetProps(body)
	if err != nil {
		return s.encodeGetProps(ecError, 0, nil)
	}
	r := s.getPropsCore(req)
	return s.encodeGetProps(r.result, r.codePage, r.row)
}

// getPropsResult is the transport-neutral outcome of GetProps: a result code,
// the echoed code page, and the single property row (PT_ERROR markers ride
// inside it on a warn-with-errors).
type getPropsResult struct {
	result   uint32
	codePage uint32
	row      mapi.PropertyValues
}

// getPropsCore runs the GetProps semantics on a decoded request,
// transport-neutral: the MAPI/HTTP handler and the RPC/HTTP stub share it.
func (s *Server) getPropsCore(req getPropsRequest) getPropsResult {
	if req.stat.codePage == cpWinUnicode {
		return getPropsResult{result: ecNotSupported, codePage: req.stat.codePage}
	}
	if len(req.proptags) > 100 {
		return getPropsResult{result: ecTableTooBig, codePage: req.stat.codePage}
	}

	g := s.snapshot()
	u, ok := g.resolveEntry(req.stat.curRec)
	if !ok {
		tags := req.proptags
		if !req.hasTags {
			tags = defaultColumns
		}
		row, _ := projectProps(nil, tags)
		return getPropsResult{result: ecWarnWithErrors, codePage: req.stat.codePage, row: row}
	}

	bag := galUserProps(u)
	if !req.hasTags {
		return getPropsResult{result: ecSuccess, codePage: req.stat.codePage, row: bag}
	}
	row, hasErr := projectProps(bag, req.proptags)
	result := ecSuccess
	if hasErr {
		result = ecWarnWithErrors
	}
	return getPropsResult{result: result, codePage: req.stat.codePage, row: row}
}

// projectProps builds a GetProps row: each requested tag maps to the entry's
// value, or a PT_ERROR(ecNotFound) marker when the entry has no such property.
// hasErr reports whether any marker was produced.
func projectProps(bag mapi.PropertyValues, tags []mapi.PropTag) (row mapi.PropertyValues, hasErr bool) {
	for _, tag := range tags {
		if v, ok := bag.Get(tag); ok {
			row = append(row, mapi.TaggedPropVal{Tag: tag, Value: v})
		} else {
			row = append(row, mapi.TaggedPropVal{Tag: errorTag(tag), Value: ecNotFound})
			hasErr = true
		}
	}
	return row, hasErr
}

// errorTag rewrites a proptag's type to PT_ERROR, the form a row carries for a
// requested-but-absent property (its value is then the SCODE).
func errorTag(tag mapi.PropTag) mapi.PropTag {
	return mapi.PropTag(uint32(tag)&0xFFFF0000 | uint32(mapi.PtError))
}

// encodeGetProps frames a GetProps response: status + result + the echoed code
// page + the single row as a self-describing TPROPVAL_ARRAY on a success or a
// warn-with-errors (the PT_ERROR markers ride inside it), else a single 0, then
// an empty AuxiliaryBuffer.
func (s *Server) encodeGetProps(result, codePage uint32, row mapi.PropertyValues) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)        // status
	p.Uint32(result)   // result
	p.Uint32(codePage) // echoed code page
	if result != ecSuccess && result != ecWarnWithErrors {
		p.Uint8(0)
	} else {
		p.Uint8(0xFF)
		_ = p.PropertyValuesLong(row)
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}
