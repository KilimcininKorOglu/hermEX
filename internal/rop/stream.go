package rop

import (
	"errors"
	"unicode/utf16"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// errNoStreamProp marks a property that is absent on the object a stream was
// opened over.
var errNoStreamProp = errors.New("rop: stream property not present")

// streamState is an open property stream: the property's bytes held in memory
// and a forward read cursor. v1 streams are read-only snapshots.
type streamState struct {
	data []byte
	pos  int
}

// streamBytes renders a property value as its stream byte form: binary verbatim,
// a Unicode string as UTF-16LE, a string8 as its code-page bytes.
func streamBytes(typ mapi.PropType, v any) []byte {
	switch typ {
	case mapi.PtBinary, mapi.PtObject:
		if b, ok := v.([]byte); ok {
			return b
		}
	case mapi.PtUnicode:
		if str, ok := v.(string); ok {
			b := make([]byte, 0, len(str)*2)
			for _, u := range utf16.Encode([]rune(str)) {
				b = append(b, byte(u), byte(u>>8))
			}
			return b
		}
	case mapi.PtString8:
		if str, ok := v.(string); ok {
			return []byte(str)
		}
	}
	return nil
}

// streamData reads the bytes a stream exposes for the property tag on the parent
// object. v1 streams a message property; the attachment branch lands with the
// attachment ROPs.
func (s *Session) streamData(parent *object, tag mapi.PropTag) ([]byte, error) {
	if parent.kind == kindMessage && parent.store != nil {
		props, err := parent.store.GetMessageProperties(parent.messageID, tag)
		if err != nil {
			return nil, err
		}
		v, ok := props.Get(tag)
		if !ok {
			return nil, errNoStreamProp
		}
		return streamBytes(tag.Type(), v), nil
	}
	return nil, errNoStreamProp
}

// ropOpenStream handles RopOpenStream ([MS-OXCPRPT] 2.2.2.14): it snapshots the
// property's bytes into a stream object and returns the stream size.
func (s *Session) ropOpenStream(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8() // OutputHandleIndex
	proptag, e2 := p.Uint32()
	_, e3 := p.Uint8() // OpenModeFlags
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	parent := s.get(handleAt(handles, hindex))
	if parent == nil {
		writeErr(out, ropOpenStream, ohindex, ecError)
		return true
	}
	data, err := s.streamData(parent, mapi.PropTag(proptag))
	if err != nil {
		writeErr(out, ropOpenStream, ohindex, ecNotFound)
		return true
	}
	h := s.alloc(&object{kind: kindStream, stream: &streamState{data: data}})
	setHandle(handles, ohindex, h)

	out.Uint8(ropOpenStream)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint32(uint32(len(data))) // StreamSize
	return true
}

// ropReadStream handles RopReadStream ([MS-OXCPRPT] 2.2.2.16): it returns the
// next chunk from the stream cursor. The request ByteCount is a 16-bit count
// unless it is the 0xBABE sentinel, in which case a 32-bit MaxByteCount follows.
// The response is a 16-bit-prefixed binary, so a single read yields at most
// 0xFFFF bytes.
func (s *Session) ropReadStream(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	byteCount, e1 := p.Uint16()
	if e1 != nil {
		return false
	}
	want := int(byteCount)
	if byteCount == 0xBABE {
		mbc, e2 := p.Uint32()
		if e2 != nil {
			return false
		}
		want = int(mbc)
	}
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.kind != kindStream || obj.stream == nil {
		writeErr(out, ropReadStream, hindex, ecError)
		return true
	}
	st := obj.stream
	// remaining and want are both non-negative, so n is in [0, 0xFFFF].
	n := min(want, len(st.data)-st.pos, 0xFFFF)
	chunk := st.data[st.pos : st.pos+n]
	st.pos += n

	out.Uint8(ropReadStream)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	_ = out.BinShort(chunk) // p_bin_s: u16 cb + data (n <= 0xFFFF)
	return true
}
