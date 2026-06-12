package ext

import "hermex/internal/mapi"

// PCL writes a predecessor change list (PCL::serialize): a bare sequence of
// size-prefixed XIDs with no outer count. Each entry is a one-byte total XID
// size (17..24) followed by the XID's GUID and local id.
func (p *Push) PCL(xids []mapi.XID) error {
	for _, x := range xids {
		size := 16 + len(x.LocalID)
		if size < 17 || size > 24 {
			return ErrFormat
		}
		p.Uint8(uint8(size))
		if err := p.XID(x); err != nil {
			return err
		}
	}
	return nil
}

// PCL reads a predecessor change list (PCL::deserialize). Because the list
// carries no count, it runs until the buffer is exhausted; a truncated final
// entry is a format error.
func (p *Pull) PCL() ([]mapi.XID, error) {
	var out []mapi.XID
	for p.Remaining() > 0 {
		size, err := p.Uint8()
		if err != nil {
			return nil, err
		}
		if size < 17 || size > 24 {
			return nil, ErrFormat
		}
		x, err := p.XID(int(size))
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}
