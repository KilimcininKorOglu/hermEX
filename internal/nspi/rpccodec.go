package nspi

// This file is the NSPI address-book NDR codec ([MS-OXNSPI] over DCE/RPC), the
// wire NSPI speaks under RPC-over-HTTP ("Outlook Anywhere"). It is a DIFFERENT
// encoding from the MAPI/HTTP NSPI body the rest of this package serializes via
// internal/ext: NDR adds natural-boundary alignment, message-global referent-id
// pointers, and conformant-array length prefixes. The transport is classic
// NDR32, so trailer_align/union_align emit NOTHING — there is no trailing pad,
// and no "doubled" length. The shared semantics (the typed cores extracted in
// the MAPI/HTTP handlers) are reused; only this (de)serialization is new.
//
// The per-op IN-decode / OUT-encode and the opnum dispatch live with the RPC
// stub; this file holds the reusable NSPI type codecs the stub composes.

import (
	"fmt"
	"unicode/utf16"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// encodeUTF16LE returns s as UTF-16LE bytes INCLUDING the two-byte terminator,
// the form an NSPI PtypString value carries. The conformant count is then
// len/2 (code units, terminator included).
func encodeUTF16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(units)*2+2)
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return append(out, 0, 0) // NUL terminator
}

// trimNUL drops a single trailing NUL from a code-page (PtypString8) value's
// bytes; the conformant count includes the terminator.
func trimNUL(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		return string(b[:n-1])
	}
	return string(b)
}

// decodeUTF16LE decodes UTF-16LE bytes (terminator included) to a string,
// dropping a trailing NUL.
func decodeUTF16LE(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
	}
	s := string(utf16.Decode(units))
	if n := len(s); n > 0 && s[n-1] == 0 {
		s = s[:n-1]
	}
	return s
}

// pushStatNDR writes a STAT ([MS-OXNSPI] 2.3.7): a leading align(4) then nine
// 4-byte fields (delta is signed). NDR32 adds no trailing pad.
func pushStatNDR(p *ndr.Push, s stat) {
	p.Uint32(s.sortType)
	p.Uint32(s.containerID)
	p.Uint32(s.curRec)
	p.Int32(s.delta)
	p.Uint32(s.numPos)
	p.Uint32(s.totalRec)
	p.Uint32(s.codePage)
	p.Uint32(s.tplLocale)
	p.Uint32(s.sortLocale)
}

// pullStatNDR reads a STAT written by pushStatNDR.
func pullStatNDR(p *ndr.Pull) (stat, error) {
	var s stat
	fields := []*uint32{&s.sortType, &s.containerID, &s.curRec, nil, &s.numPos,
		&s.totalRec, &s.codePage, &s.tplLocale, &s.sortLocale}
	for i, dst := range fields {
		v, err := p.Uint32()
		if err != nil {
			return stat{}, err
		}
		if i == 3 {
			s.delta = int32(v) // signed
			continue
		}
		*dst = v
	}
	return s, nil
}

// pushCtxHandleNDR writes a 20-byte CONTEXT_HANDLE: handle_type(u32) + GUID. The
// NSPI session is keyed by this handle.
func pushCtxHandleNDR(p *ndr.Push, handleType uint32, guid mapi.GUID) {
	p.Uint32(handleType)
	p.GUID(guid)
}

// pullCtxHandleNDR reads a CONTEXT_HANDLE.
func pullCtxHandleNDR(p *ndr.Pull) (handleType uint32, guid mapi.GUID, err error) {
	if handleType, err = p.Uint32(); err != nil {
		return 0, mapi.GUID{}, err
	}
	guid, err = p.GUID()
	return handleType, guid, err
}

// pushU32ArrayNDR writes a PropertyTagArray_r / MID array ([MS-OXNSPI] 2.3.2):
// a conformant max_count of N+1, then cValues(N) + offset(0) + length(N) + the N
// 4-byte elements. The deliberate N+1 conformance size matches the reference and
// the IDL dimension.
func pushU32ArrayNDR(p *ndr.Push, vals []uint32) {
	n := uint32(len(vals))
	p.Uint32(n + 1) // conformant max_count = N+1
	p.Uint32(n)     // cValues
	p.Uint32(0)     // offset
	p.Uint32(n)     // actual length
	for _, v := range vals {
		p.Uint32(v)
	}
}

// pullU32ArrayNDR reads a PropertyTagArray_r / MID array. It enforces the
// reference's invariants: max_count == cValues+1, offset == 0, length == cValues.
func pullU32ArrayNDR(p *ndr.Pull) ([]uint32, error) {
	maxCount, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	cValues, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	offset, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	length, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if maxCount != cValues+1 || offset != 0 || length != cValues {
		return nil, fmt.Errorf("%w: proptag array shape (max=%d cValues=%d off=%d len=%d)", ndr.ErrFormat, maxCount, cValues, offset, length)
	}
	out := make([]uint32, cValues)
	for i := range out {
		if out[i], err = p.Uint32(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// pullInlineMIDArrayNDR reads the QueryRows-IN explicit MID array, the ONE NSPI
// array pulled inline rather than via pullU32ArrayNDR: a standalone count(u32) +
// a referent + (if non-null) a conformant max_count that MUST equal the count,
// then the count 4-byte MIds. There is NO N+1, offset, or length word here — do
// not route this through the proptag-array helper.
func pullInlineMIDArrayNDR(p *ndr.Pull) ([]uint32, error) {
	count, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	ref, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if ref == 0 {
		return nil, nil
	}
	maxCount, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if maxCount != count {
		return nil, fmt.Errorf("%w: inline MID array count=%d max_count=%d", ndr.ErrFormat, count, maxCount)
	}
	out := make([]uint32, count)
	for i := range out {
		if out[i], err = p.Uint32(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// pushProptagsNDR writes a proptag array (the u32-array shape) from PropTags.
func pushProptagsNDR(p *ndr.Push, tags []mapi.PropTag) {
	vals := make([]uint32, len(tags))
	for i, t := range tags {
		vals[i] = uint32(t)
	}
	pushU32ArrayNDR(p, vals)
}

// pushPropValHeaderNDR writes the header half of a PROPERTY_VALUE: proptag(u32)
// + reserved(u32) + ptype(u32) + the inline scalar (or a referent id for a
// pointer type whose bytes follow in the content pass). It errors on a type the
// GAL never emits rather than encoding a wrong shape silently.
func pushPropValHeaderNDR(p *ndr.Push, tag mapi.PropTag, value any) error {
	p.Uint32(uint32(tag))
	p.Uint32(0) // reserved
	p.Uint32(uint32(tag.Type()))
	switch tag.Type() {
	case mapi.PtLong:
		v, ok := value.(int32)
		if !ok {
			return fmt.Errorf("%w: PtLong value is %T", ndr.ErrFormat, value)
		}
		p.Uint32(uint32(v))
	case mapi.PtBoolean:
		v, ok := value.(bool)
		if !ok {
			return fmt.Errorf("%w: PtBoolean value is %T", ndr.ErrFormat, value)
		}
		if v {
			p.Uint8(1)
		} else {
			p.Uint8(0)
		}
	case mapi.PtError:
		v, ok := value.(uint32)
		if !ok {
			return fmt.Errorf("%w: PtError value is %T", ndr.ErrFormat, value)
		}
		p.Uint32(v)
	case mapi.PtUnicode, mapi.PtString8:
		p.UniquePtr(true) // string bytes follow in the content pass
	case mapi.PtBinary:
		v, ok := value.([]byte)
		if !ok {
			return fmt.Errorf("%w: PtBinary value is %T", ndr.ErrFormat, value)
		}
		p.Uint32(uint32(len(v))) // cb
		p.UniquePtr(true)        // bytes follow in the content pass
	case mapi.PtMvBinary:
		v, ok := value.([][]byte)
		if !ok {
			return fmt.Errorf("%w: PtMvBinary value is %T", ndr.ErrFormat, value)
		}
		p.Uint32(uint32(len(v))) // count of entries
		p.UniquePtr(true)        // the array follows in the content pass
	default:
		return fmt.Errorf("%w: NDR cannot encode property type %#04x", ndr.ErrFormat, uint16(tag.Type()))
	}
	return nil
}

// pushPropValContentNDR writes the content half of a PROPERTY_VALUE: the
// deferred bytes of a pointer type (a conformant-varying string or a conformant
// binary). Scalars wrote everything in the header pass, so they emit nothing.
func pushPropValContentNDR(p *ndr.Push, tag mapi.PropTag, value any) error {
	switch tag.Type() {
	case mapi.PtUnicode:
		v, _ := value.(string)
		b := encodeUTF16LE(v)
		n := uint32(len(b) / 2) // code units, terminator included
		p.Uint32(n)             // max_count
		p.Uint32(0)             // offset
		p.Uint32(n)             // actual_count
		p.Raw(b)
	case mapi.PtString8:
		v, _ := value.(string)
		b := append([]byte(v), 0) // NUL terminator
		n := uint32(len(b))
		p.Uint32(n) // max_count
		p.Uint32(0) // offset
		p.Uint32(n) // actual_count
		p.Raw(b)
	case mapi.PtBinary:
		v, _ := value.([]byte)
		p.Uint32(uint32(len(v))) // conformant max_count
		p.Raw(v)
	case mapi.PtMvBinary:
		// BINARY_ARRAY: max_count, then every entry's header (cb + referent),
		// then every entry's content (conformant max_count + raw bytes).
		v, _ := value.([][]byte)
		p.Uint32(uint32(len(v))) // max_count = count
		for _, b := range v {
			p.Uint32(uint32(len(b))) // cb
			p.UniquePtr(true)
		}
		for _, b := range v {
			p.Uint32(uint32(len(b))) // conformant max_count
			p.Raw(b)
		}
	}
	return nil
}

// pullPropValNDR reads a standalone PROPERTY_VALUE (header and content
// contiguous, as the reference pulls a SeekEntries target or a restriction
// value with both flags set). It returns the value typed by the proptag.
func pullPropValNDR(p *ndr.Pull) (mapi.TaggedPropVal, error) {
	var tv mapi.TaggedPropVal
	tag, err := p.Uint32()
	if err != nil {
		return tv, err
	}
	if _, err = p.Uint32(); err != nil { // reserved
		return tv, err
	}
	ptype, err := p.Uint32()
	if err != nil {
		return tv, err
	}
	tv.Tag = mapi.PropTag(tag)
	if uint16(ptype) != uint16(mapi.PropTag(tag).Type()) {
		return tv, fmt.Errorf("%w: property value type %#x != tag type %#x", ndr.ErrFormat, ptype, uint32(mapi.PropTag(tag).Type()))
	}
	switch mapi.PropTag(tag).Type() {
	case mapi.PtShort:
		v, err := p.Uint16()
		if err != nil {
			return tv, err
		}
		tv.Value = int16(v)
	case mapi.PtLong:
		v, err := p.Uint32()
		if err != nil {
			return tv, err
		}
		tv.Value = int32(v)
	case mapi.PtBoolean:
		v, err := p.Uint8()
		if err != nil {
			return tv, err
		}
		tv.Value = v != 0
	case mapi.PtError:
		v, err := p.Uint32()
		if err != nil {
			return tv, err
		}
		tv.Value = v
	case mapi.PtUnicode:
		s, err := pullConfStringNDR(p, true)
		if err != nil {
			return tv, err
		}
		tv.Value = s
	case mapi.PtString8:
		s, err := pullConfStringNDR(p, false)
		if err != nil {
			return tv, err
		}
		tv.Value = s
	case mapi.PtBinary:
		b, err := pullConfBinaryNDR(p)
		if err != nil {
			return tv, err
		}
		tv.Value = b
	default:
		return tv, fmt.Errorf("%w: NDR cannot decode property type %#04x", ndr.ErrFormat, uint16(mapi.PropTag(tag).Type()))
	}
	return tv, nil
}

// pullConfStringNDR reads a referent + (if non-null) a conformant-varying string
// (max_count + offset + actual_count + chars). wide selects UTF-16 vs 8-bit.
func pullConfStringNDR(p *ndr.Pull, wide bool) (string, error) {
	ref, err := p.Uint32()
	if err != nil {
		return "", err
	}
	if ref == 0 {
		return "", nil
	}
	if _, err = p.Uint32(); err != nil { // max_count
		return "", err
	}
	if _, err = p.Uint32(); err != nil { // offset
		return "", err
	}
	actual, err := p.Uint32()
	if err != nil {
		return "", err
	}
	width := int(actual)
	if wide {
		width *= 2
	}
	raw, err := p.Raw(width)
	if err != nil {
		return "", err
	}
	if wide {
		return decodeUTF16LE(raw), nil
	}
	return trimNUL(raw), nil
}

// pullConfBinaryNDR reads a PtypBinary value body: cb(u32) + referent + (if
// non-null) conformant max_count + the bytes.
func pullConfBinaryNDR(p *ndr.Pull) ([]byte, error) {
	cb, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	ref, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if ref == 0 {
		return nil, nil
	}
	maxCount, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if maxCount != cb {
		return nil, fmt.Errorf("%w: binary cb=%d max_count=%d", ndr.ErrFormat, cb, maxCount)
	}
	return p.Raw(int(cb))
}

// pushRowSetNDR writes a PROPROW_SET ([MS-OXNSPI] 2.3.4.2): the conformant
// max_count, the row count, then every row's header (reserved + cValues +
// referent) followed by every row's content (the projected PROPERTY_VALUEs).
// Each row is projected against cols, so a column the row lacks becomes a
// PT_ERROR(ecNotFound) value — the same projection the MAPI/HTTP encoder uses.
func pushRowSetNDR(p *ndr.Push, cols []mapi.PropTag, rows []mapi.PropertyValues) error {
	projected := make([]mapi.PropertyValues, len(rows))
	for i, r := range rows {
		projected[i], _ = projectProps(r, cols)
	}
	crows := uint32(len(projected))
	p.Uint32(crows) // conformant max_count
	p.Uint32(crows) // actual count
	for _, vals := range projected {
		p.Uint32(0)                 // reserved
		p.Uint32(uint32(len(vals))) // cValues
		p.UniquePtr(true)           // values referent (bytes follow in the content pass)
	}
	for _, vals := range projected {
		p.Uint32(uint32(len(vals))) // conformant max_count of the value array
		for _, v := range vals {
			if err := pushPropValHeaderNDR(p, v.Tag, v.Value); err != nil {
				return err
			}
		}
		for _, v := range vals {
			if err := pushPropValContentNDR(p, v.Tag, v.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

// pushPropertyRowNDR writes a single PROPERTY_ROW (GetProps OUT, not a rowset):
// header (reserved + cValues + referent) then content (the value array). It
// projects the row against cols like pushRowSetNDR does per row.
func pushPropertyRowNDR(p *ndr.Push, cols []mapi.PropTag, row mapi.PropertyValues) error {
	vals, _ := projectProps(row, cols)
	p.Uint32(0)                 // reserved
	p.Uint32(uint32(len(vals))) // cValues
	p.UniquePtr(true)           // values referent
	p.Uint32(uint32(len(vals))) // conformant max_count
	for _, v := range vals {
		if err := pushPropValHeaderNDR(p, v.Tag, v.Value); err != nil {
			return err
		}
	}
	for _, v := range vals {
		if err := pushPropValContentNDR(p, v.Tag, v.Value); err != nil {
			return err
		}
	}
	return nil
}

// pullStringsArrayNDR reads a conformant array of strings ([MS-OXNSPI]
// StringsArray_r / WStringsArray_r): count(u32) + referent, then count
// per-element referents, then each present element's conformant-varying string.
// wide selects UTF-16 (ResolveNamesW) vs 8-bit (DNToMId / ResolveNames).
func pullStringsArrayNDR(p *ndr.Pull, wide bool) ([]string, error) {
	count, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	ref, err := p.Uint32() // array referent
	if err != nil {
		return nil, err
	}
	if ref == 0 {
		return nil, nil
	}
	maxCount, err := p.Uint32() // conformant max_count
	if err != nil {
		return nil, err
	}
	if maxCount != count {
		return nil, fmt.Errorf("%w: strings array count=%d max_count=%d", ndr.ErrFormat, count, maxCount)
	}
	present := make([]bool, count)
	for i := range present {
		r, err := p.Uint32() // per-element referent
		if err != nil {
			return nil, err
		}
		present[i] = r != 0
	}
	out := make([]string, count)
	for i := range out {
		if !present[i] {
			continue
		}
		if _, err = p.Uint32(); err != nil { // max_count
			return nil, err
		}
		if _, err = p.Uint32(); err != nil { // offset
			return nil, err
		}
		actual, err := p.Uint32()
		if err != nil {
			return nil, err
		}
		width := int(actual)
		if wide {
			width *= 2
		}
		raw, err := p.Raw(width)
		if err != nil {
			return nil, err
		}
		if wide {
			out[i] = decodeUTF16LE(raw)
		} else {
			out[i] = trimNUL(raw)
		}
	}
	return out, nil
}
