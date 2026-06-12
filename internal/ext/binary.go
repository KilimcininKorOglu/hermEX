package ext

import "hermex/internal/mapi"

// --- binary blob variants (fixed prefix width, independent of FlagWCount) ---
//
// There are three blob codecs beside the flag-gated Bin:
//   - a short form with an always-16-bit count,
//   - an extended form with an always-32-bit count,
//   - a raw form with no count that runs to the end of the buffer.
// These mirror those exactly so callers can pick the width a structure mandates
// rather than inheriting the ambient FlagWCount regime.

// BinShort writes a blob with a 16-bit length prefix regardless of FlagWCount.
// A value too large for the prefix is rejected.
func (p *Push) BinShort(b []byte) error {
	if len(b) > 0xFFFF {
		return ErrFormat
	}
	p.Uint16(uint16(len(b)))
	p.Raw(b)
	return nil
}

// BinShort reads a 16-bit-prefixed blob written by BinShort.
func (p *Pull) BinShort() ([]byte, error) {
	n, err := p.Uint16()
	if err != nil {
		return nil, err
	}
	return p.Raw(int(n))
}

// BinEx writes a blob with a 32-bit length prefix regardless of FlagWCount.
func (p *Push) BinEx(b []byte) {
	p.Uint32(uint32(len(b)))
	p.Raw(b)
}

// BinEx reads a 32-bit-prefixed blob written by BinEx.
func (p *Pull) BinEx() ([]byte, error) {
	n, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	return p.Raw(int(n))
}

// Blob writes raw bytes with no length prefix; the reader must
// know the length out of band (it consumes the rest of the buffer).
func (p *Push) Blob(b []byte) { p.Raw(b) }

// Blob reads every remaining byte (which consumes
// m_data_size - m_offset). It returns a fresh copy and never underflows.
func (p *Pull) Blob() ([]byte, error) { return p.Raw(p.Remaining()) }

// --- SYSTEMTIME (eight little-endian int16 calendar fields) ---

// SystemTime writes a 16-byte SYSTEMTIME: eight little-endian int16 fields in
// year, month, day-of-week, day, hour, minute, second, millisecond order.
func (p *Push) SystemTime(t mapi.SystemTime) {
	p.Uint16(uint16(t.Year))
	p.Uint16(uint16(t.Month))
	p.Uint16(uint16(t.DayOfWeek))
	p.Uint16(uint16(t.Day))
	p.Uint16(uint16(t.Hour))
	p.Uint16(uint16(t.Minute))
	p.Uint16(uint16(t.Second))
	p.Uint16(uint16(t.Milliseconds))
}

// SystemTime reads a 16-byte SYSTEMTIME written by SystemTime.
func (p *Pull) SystemTime() (mapi.SystemTime, error) {
	var t mapi.SystemTime
	fields := [...]*int16{
		&t.Year, &t.Month, &t.DayOfWeek, &t.Day,
		&t.Hour, &t.Minute, &t.Second, &t.Milliseconds,
	}
	for _, f := range fields {
		v, err := p.Uint16()
		if err != nil {
			return mapi.SystemTime{}, err
		}
		*f = int16(v)
	}
	return t, nil
}
