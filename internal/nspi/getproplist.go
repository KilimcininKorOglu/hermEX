package nspi

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// getPropListRequest is the decoded GetPropList body ([MS-OXNSPI] 2.2.4 /
// [MS-OXCMAPIHTTP] 2.2.5.5): flags, the MId whose property tags to list, the
// code page, then the AuxiliaryBuffer. The request carries no strings or arrays,
// so its layout is flag-invariant.
type getPropListRequest struct {
	flags    uint32
	mid      uint32
	codePage uint32
}

func pullGetPropList(body []byte) (getPropListRequest, error) {
	p := ext.NewPull(body, abkFlags)
	var r getPropListRequest
	var err error
	if r.flags, err = p.Uint32(); err != nil {
		return r, err
	}
	if r.mid, err = p.Uint32(); err != nil {
		return r, err
	}
	if r.codePage, err = p.Uint32(); err != nil {
		return r, err
	}
	return r, skipAuxIn(p)
}

// GetPropList handles the NSPI GetPropList request ([MS-OXNSPI] 2.2.4): it
// returns the set of property tags available for the entry at the given MId.
// Every v1 GAL entry exposes the same fixed address-book column set, so a valid
// MId yields defaultColumns; MId 0 and any MId without an entry are an invalid
// object.
func (s *Server) GetPropList(body []byte) []byte {
	req, err := pullGetPropList(body)
	if err != nil {
		return s.encodeGetPropList(ecError, nil)
	}
	r := s.getPropListCore(req)
	return s.encodeGetPropList(r.result, r.tags)
}

// getPropListResult is the transport-neutral outcome of GetPropList: a result
// code and the available property-tag set for the entry.
type getPropListResult struct {
	result uint32
	tags   []mapi.PropTag
}

// getPropListCore runs the GetPropList semantics on a decoded request,
// transport-neutral: the MAPI/HTTP handler and the RPC/HTTP stub share it.
func (s *Server) getPropListCore(req getPropListRequest) getPropListResult {
	if req.mid == 0 {
		return getPropListResult{result: ecInvalidObject}
	}
	g := s.snapshot()
	if _, ok := g.byMID(req.mid); !ok {
		return getPropListResult{result: ecInvalidObject}
	}
	return getPropListResult{result: ecSuccess, tags: defaultColumns}
}

// encodeGetPropList frames a GetPropList response: status + result + the
// property-tag array on success (else a single 0), then an empty
// AuxiliaryBuffer.
func (s *Server) encodeGetPropList(result uint32, tags []mapi.PropTag) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)      // status
	p.Uint32(result) // result
	if result != ecSuccess {
		p.Uint8(0)
	} else {
		p.Uint8(0xFF)
		_ = p.PropTagsLong(tags)
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}
