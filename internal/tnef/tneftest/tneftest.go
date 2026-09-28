// Package tneftest builds TNEF streams for tests. It is test support only and is
// never imported by production code.
package tneftest

import (
	"bytes"
	"encoding/binary"
	"unicode/utf16"

	"hermex/internal/mapi"
)

// Attribute ids used by the builders ([MS-OXTNEF] 2.1.3.2).
const (
	AttTnefVersion    = 0x00089006
	AttOemCodepage    = 0x00069007
	AttMessageClass   = 0x00078008
	AttSubject        = 0x00018004
	AttMsgProps       = 0x00069003
	AttRecipTable     = 0x00069004
	AttAttachRendData = 0x00069002
	AttAttachData     = 0x0006800F
	AttAttachTitle    = 0x00018010
	AttAttachment     = 0x00069005
)

// Stream accumulates a TNEF stream.
type Stream struct{ b bytes.Buffer }

// New starts a stream with its signature, key, version and code page 1252.
func New() *Stream {
	s := &Stream{}
	s.u32(0x223E9F78)
	s.u16(0)
	s.Attr(1, AttTnefVersion, le32(0x00010000))
	s.Attr(1, AttOemCodepage, append(le32(1252), le32(0)...))
	return s
}

// Attr appends one attribute with its correct checksum.
func (s *Stream) Attr(level byte, id uint32, data []byte) *Stream {
	var sum uint16
	for _, c := range data {
		sum += uint16(c)
	}
	return s.RawAttr(level, id, data, sum)
}

// RawAttr appends one attribute with the given checksum.
func (s *Stream) RawAttr(level byte, id uint32, data []byte, sum uint16) *Stream {
	s.b.WriteByte(level)
	s.u32(id)
	s.u32(uint32(len(data)))
	s.b.Write(data)
	s.u16(sum)
	return s
}

// Bytes returns the stream.
func (s *Stream) Bytes() []byte { return s.b.Bytes() }

func (s *Stream) u16(v uint16) { s.b.Write(binary.LittleEndian.AppendUint16(nil, v)) }
func (s *Stream) u32(v uint32) { s.b.Write(binary.LittleEndian.AppendUint32(nil, v)) }

// CString is a null-terminated 8-bit string.
func CString(v string) []byte { return append([]byte(v), 0) }

// Props encodes a property list: count, then each property.
type Props struct {
	b bytes.Buffer
	n uint32
}

// Bytes returns the encoded list.
func (p *Props) Bytes() []byte { return append(le32(p.n), p.b.Bytes()...) }

// Unicode adds a PtUnicode property.
func (p *Props) Unicode(tag mapi.PropTag, v string) *Props {
	p.tag(tag, nil)
	p.variable(utf16z(v))
	return p
}

// Binary adds a PtBinary property.
func (p *Props) Binary(tag mapi.PropTag, v []byte) *Props {
	p.tag(tag, nil)
	p.variable(v)
	return p
}

// Long adds a PtLong property.
func (p *Props) Long(tag mapi.PropTag, v int32) *Props {
	p.tag(tag, nil)
	p.b.Write(le32(uint32(v)))
	return p
}

// NamedUnicode adds a PtUnicode named property under a string name.
func (p *Props) NamedUnicode(name mapi.PropertyName, v string) *Props {
	p.tag(mapi.MakeTag(0x8000, mapi.PtUnicode), &name)
	p.variable(utf16z(v))
	return p
}

// Object adds a PtObject property whose data follows the interface id.
func (p *Props) Object(tag mapi.PropTag, iid mapi.GUID, data []byte) *Props {
	p.tag(tag, nil)
	p.variable(append(guid(iid), data...))
	return p
}

// tag writes a property tag and, for a named property, its name.
func (p *Props) tag(tag mapi.PropTag, name *mapi.PropertyName) {
	p.n++
	p.b.Write(binary.LittleEndian.AppendUint16(nil, uint16(tag.Type())))
	p.b.Write(binary.LittleEndian.AppendUint16(nil, tag.ID()))
	if name == nil {
		return
	}
	p.b.Write(guid(name.GUID))
	if name.Kind == mapi.MnidID {
		p.b.Write(le32(0))
		p.b.Write(le32(name.LID))
		return
	}
	raw := utf16z(name.Name)
	p.b.Write(le32(1))
	p.b.Write(le32(uint32(len(raw))))
	p.b.Write(raw)
	p.b.Write(make([]byte, (4-len(raw)%4)%4))
}

// variable writes a counted, length-prefixed, padded value.
func (p *Props) variable(v []byte) {
	p.b.Write(le32(1))
	p.b.Write(le32(uint32(len(v))))
	p.b.Write(v)
	p.b.Write(make([]byte, (4-len(v)%4)%4))
}

// RecipTable encodes attRecipTable from property lists.
func RecipTable(rows ...*Props) []byte {
	out := le32(uint32(len(rows)))
	for _, r := range rows {
		out = append(out, r.Bytes()...)
	}
	return out
}

// RendData is an attAttachRendData value for a file attachment.
func RendData() []byte {
	return append(append([]byte{1, 0}, le32(0xFFFFFFFF)...), 0, 0, 0, 0, 0, 0, 0, 0)
}

func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func utf16z(v string) []byte {
	var out []byte
	for _, c := range utf16.Encode([]rune(v)) {
		out = binary.LittleEndian.AppendUint16(out, c)
	}
	return append(out, 0, 0)
}

func guid(g mapi.GUID) []byte {
	out := binary.LittleEndian.AppendUint32(nil, g.Data1)
	out = binary.LittleEndian.AppendUint16(out, g.Data2)
	out = binary.LittleEndian.AppendUint16(out, g.Data3)
	return append(out, g.Data4[:]...)
}
