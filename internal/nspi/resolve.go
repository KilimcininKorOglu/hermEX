package nspi

import (
	"strings"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// pullStringArray reads a STRING_ARRAY (u32 count + per-element NUL-terminated
// String8). The names in DNToMId carry no per-element presence byte (the
// reference reads them outside the address-book encoding).
func pullStringArray(p *ext.Pull) ([]string, error) {
	n, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	out := make([]string, n)
	for i := range out {
		if out[i], err = p.String8(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// pullWStringArray reads a WSTRING_ARRAY (u32 count + per-element NUL-terminated
// UTF-16LE). ResolveNamesW reads its names with the address-book encoding
// disabled, so there is no per-element presence byte either.
func pullWStringArray(p *ext.Pull) ([]string, error) {
	n, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	out := make([]string, n)
	for i := range out {
		if out[i], err = p.Unicode(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// stripTypePrefix drops a leading "TYPE:" qualifier (e.g. "SMTP:user@host" ->
// "user@host"), matching the reference's resolve handling.
func stripTypePrefix(name string) string {
	if _, after, found := strings.Cut(name, ":"); found {
		return after
	}
	return name
}

// midArray pushes a MID_ARRAY (the LPROPTAG_ARRAY shape: u32 count + u32[]).
func midArray(p *ext.Push, mids []uint32) error {
	tags := make([]mapi.PropTag, len(mids))
	for i, m := range mids {
		tags[i] = mapi.PropTag(m)
	}
	return p.PropTagsLong(tags)
}

// DNToMId handles the NSPI DNToMId request ([MS-OXNSPI] 2.2.4): it maps each
// input distinguished name to its MId (or MID_UNRESOLVED). Our DNs embed the
// SMTP address as the final cn= component, so the reverse is a snapshot lookup.
func (s *Server) DNToMId(body []byte) []byte {
	p := ext.NewPull(body, abkFlags)
	if _, err := p.Uint32(); err != nil { // reserved
		return s.encodeDNToMId(ecError, nil)
	}
	hasNames, err := p.Uint8()
	if err != nil {
		return s.encodeDNToMId(ecError, nil)
	}
	var names []string
	if hasNames != 0 {
		if names, err = pullStringArray(p); err != nil {
			return s.encodeDNToMId(ecError, nil)
		}
	}
	if err := skipAuxIn(p); err != nil {
		return s.encodeDNToMId(ecError, nil)
	}

	g := s.snapshot()
	mids := make([]uint32, len(names))
	for i, dn := range names {
		mids[i] = midUnresolved
		if smtp, ok := dnToSMTP(dn); ok {
			if mid, found := g.byAddress(smtp); found {
				mids[i] = mid
			}
		}
	}
	return s.encodeDNToMId(ecSuccess, mids)
}

func (s *Server) encodeDNToMId(result uint32, mids []uint32) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)      // status
	p.Uint32(result) // result
	if result != ecSuccess {
		p.Uint8(0)
	} else {
		p.Uint8(0xFF)
		_ = midArray(p, mids)
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}

// resolveNamesRequest is the decoded ResolveNamesW body ([MS-OXNSPI] 2.2.4):
// reserved + an optional STAT + an optional column set + the names to resolve.
type resolveNamesRequest struct {
	stat    stat
	columns []mapi.PropTag
	names   []string
}

func pullResolveNames(body []byte) (resolveNamesRequest, error) {
	p := ext.NewPull(body, abkFlags)
	var r resolveNamesRequest
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
	hasCols, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasCols != 0 {
		if r.columns, err = p.PropTagsLong(); err != nil {
			return r, err
		}
	}
	hasNames, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasNames != 0 {
		if r.names, err = pullWStringArray(p); err != nil {
			return r, err
		}
	}
	return r, skipAuxIn(p)
}

// ResolveNamesW handles the NSPI ResolveNamesW request ([MS-OXNSPI] 2.2.4): it
// resolves each input name against the GAL, returning a per-name result code
// (unresolved / ambiguous / resolved) and a row for each uniquely resolved name.
func (s *Server) ResolveNamesW(body []byte) []byte {
	req, err := pullResolveNames(body)
	if err != nil {
		return s.encodeResolveNames(ecError, 0, nil, nil, nil)
	}
	if req.stat.codePage == cpWinUnicode {
		return s.encodeResolveNames(ecNotSupported, req.stat.codePage, nil, nil, nil)
	}
	cols := req.columns
	if len(cols) == 0 {
		cols = defaultColumns
	}
	if len(cols) > 100 {
		return s.encodeResolveNames(ecTableTooBig, req.stat.codePage, nil, nil, nil)
	}

	g := s.snapshot()
	mids := make([]uint32, len(req.names))
	var rows []mapi.PropertyValues
	for i, name := range req.names {
		token := stripTypePrefix(name)
		if token == "" {
			mids[i] = midUnresolved
			continue
		}
		mid, status := g.resolve(token)
		mids[i] = status
		if status == midResolved {
			if u, ok := g.byMID(mid); ok {
				rows = append(rows, galUserProps(u))
			}
		}
	}
	return s.encodeResolveNames(ecSuccess, req.stat.codePage, mids, cols, rows)
}

// encodeResolveNames frames a ResolveNamesW response: status + result + the
// echoed code page + the per-name MId result array + the resolved-row set, or a
// pair of 0 markers on failure, then an empty AuxiliaryBuffer.
func (s *Server) encodeResolveNames(result, codePage uint32, mids []uint32, cols []mapi.PropTag, rows []mapi.PropertyValues) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)        // status
	p.Uint32(result)   // result
	p.Uint32(codePage) // echoed code page
	if result != ecSuccess {
		p.Uint8(0)
		p.Uint8(0)
	} else {
		p.Uint8(0xFF)
		_ = midArray(p, mids)
		p.Uint8(0xFF)
		_ = pushColRow(p, cols, rows)
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}
