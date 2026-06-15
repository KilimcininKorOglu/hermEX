package nspi

import "hermex/internal/ext"

// stat is the NSPI address-book cursor (STAT, [MS-OXNSPI] 2.2.8): 36 bytes
// carried in every request and returned updated. The server holds no cursor —
// a row position is recomputed from this each call (see the internal spec).
type stat struct {
	sortType    uint32 // SortTypeDisplayName etc.
	containerID uint32 // the AB container (the GAL container is 0)
	curRec      uint32 // MId of the current row
	delta       int32  // signed offset to apply to the position
	numPos      uint32 // fractional position numerator
	totalRec    uint32 // total rows in the container
	codePage    uint32 // client code page
	tplLocale   uint32 // template locale
	sortLocale  uint32 // sort locale
}

// pullStat reads a STAT in wire order (delta is a signed 32-bit value carried in
// the same little-endian width as the unsigned fields).
func pullStat(p *ext.Pull) (stat, error) {
	var s stat
	fields := []*uint32{&s.sortType, &s.containerID, &s.curRec, nil, &s.numPos,
		&s.totalRec, &s.codePage, &s.tplLocale, &s.sortLocale}
	for i, dst := range fields {
		v, err := p.Uint32()
		if err != nil {
			return stat{}, err
		}
		if i == 3 {
			s.delta = int32(v) // the 4th field is signed
			continue
		}
		*dst = v
	}
	return s, nil
}

// pushStat writes a STAT in wire order.
func pushStat(p *ext.Push, s stat) {
	p.Uint32(s.sortType)
	p.Uint32(s.containerID)
	p.Uint32(s.curRec)
	p.Uint32(uint32(s.delta))
	p.Uint32(s.numPos)
	p.Uint32(s.totalRec)
	p.Uint32(s.codePage)
	p.Uint32(s.tplLocale)
	p.Uint32(s.sortLocale)
}

// skipAuxIn consumes the trailing AuxiliaryBuffer every MAPI/HTTP NSPI request
// carries (cb_auxin u32 + auxin[cb_auxin]); v1 issues no auxiliary blocks and
// ignores any the client sends.
func skipAuxIn(p *ext.Pull) error {
	cb, err := p.Uint32()
	if err != nil {
		return err
	}
	if cb > 0 {
		if _, err := p.Raw(int(cb)); err != nil {
			return err
		}
	}
	return nil
}

// bindRequest is the decoded NSPI Bind body ([MS-OXNSPI] 2.2.7 /
// [MS-OXCMAPIHTTP] 2.2.5.1.1): flags + an optional STAT + the AuxiliaryBuffer.
type bindRequest struct {
	flags uint32
	stat  stat
}

// pullBindRequest decodes a Bind request body. A zero presence byte means the
// client sent no STAT, so the default (zeroed) cursor stands.
func pullBindRequest(body []byte) (bindRequest, error) {
	p := ext.NewPull(body, 0)
	var r bindRequest
	var err error
	if r.flags, err = p.Uint32(); err != nil {
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
	return r, skipAuxIn(p)
}

// pullUnbindRequest decodes an Unbind request body ([MS-OXNSPI] 2.2.8 /
// [MS-OXCMAPIHTTP] 2.2.5.2.1): a reserved u32 then the AuxiliaryBuffer. It
// carries no actionable fields.
func pullUnbindRequest(body []byte) error {
	p := ext.NewPull(body, 0)
	if _, err := p.Uint32(); err != nil { // Reserved
		return err
	}
	return skipAuxIn(p)
}
