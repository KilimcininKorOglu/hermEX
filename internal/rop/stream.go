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

// streamState is an open property stream: the property's bytes held in memory and
// a read/write cursor. A read-only stream is a snapshot of the property's bytes. A
// writable stream (opened with a write open-mode over a writable parent) buffers
// edits in data and, on CommitStream, stages them back into the parent object's
// write buffer for tag — which the parent's SaveChanges then persists.
type streamState struct {
	data     []byte
	pos      int
	writable bool
	dirty    bool
	parent   *object      // the object a writable stream commits back to
	tag      mapi.PropTag // the property the stream is bound to
}

// stream open-mode write intent: any bit of MAPI_BEST_ACCESS (0x03) — ReadWrite
// (0x01), Create (0x02), or BestAccess (0x03) — requests write access.
const streamWriteMode uint8 = 0x03

// SeekStream origins ([MS-OXCPRPT] 2.2.2.18 / STREAM_SEEK_*).
const (
	streamSeekSet uint8 = 0 // from the beginning
	streamSeekCur uint8 = 1 // from the current position
	streamSeekEnd uint8 = 2 // from the end
)

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
// object — a message property (via the store) or an opened attachment's
// property bag (e.g. PrAttachDataBin).
func (s *Session) streamData(parent *object, tag mapi.PropTag) ([]byte, error) {
	switch {
	case parent.kind == kindMessage && parent.store != nil:
		props, err := parent.store.GetMessageProperties(parent.messageID, tag)
		if err != nil {
			return nil, err
		}
		v, ok := props.Get(tag)
		if !ok {
			return nil, errNoStreamProp
		}
		return streamBytes(tag.Type(), v), nil
	case parent.kind == kindEmbedded:
		if parent.embedded == nil || parent.embedded.msg == nil {
			return nil, errNoStreamProp
		}
		v, ok := parent.embedded.msg.Props.Get(tag)
		if !ok {
			return nil, errNoStreamProp
		}
		return streamBytes(tag.Type(), v), nil
	case parent.kind == kindAttachment:
		v, ok := parent.attachProps.Get(tag)
		if !ok {
			return nil, errNoStreamProp
		}
		return streamBytes(tag.Type(), v), nil
	default:
		return nil, errNoStreamProp
	}
}

// ropOpenStream handles RopOpenStream ([MS-OXCPRPT] 2.2.2.14): it snapshots the
// property's bytes into a stream object and returns the stream size. A write
// open-mode over a writable parent (a created attachment, or a message being
// composed or edited) opens a writable stream seeded with the property's current
// bytes; a read open-mode, or any mode over a read-only parent, opens a read-only
// snapshot.
func (s *Session) ropOpenStream(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8() // OutputHandleIndex
	proptag, e2 := p.Uint32()
	mode, e3 := p.Uint8() // OpenModeFlags
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	parent := s.get(handleAt(handles, hindex))
	if parent == nil {
		writeErr(out, ropOpenStream, ohindex, ecError)
		return true
	}
	tag := mapi.PropTag(proptag)

	var st *streamState
	if mode&streamWriteMode != 0 && isStreamWritable(parent) {
		st = &streamState{data: s.writeStreamInitial(parent, tag), writable: true, parent: parent, tag: tag}
	} else {
		data, err := s.streamData(parent, tag)
		if err != nil {
			writeErr(out, ropOpenStream, ohindex, ecNotFound)
			return true
		}
		st = &streamState{data: data}
	}
	h := s.alloc(&object{kind: kindStream, stream: st})
	setHandle(handles, ohindex, h)

	out.Uint8(ropOpenStream)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint32(uint32(len(st.data))) // StreamSize
	return true
}

// isStreamWritable reports whether a property stream opened over the object can be
// written: a created attachment buffers into its pending bag, and a message being
// composed or edited (or an embedded message being composed) buffers into its
// property bag. A read-only opened attachment has no write buffer.
func isStreamWritable(o *object) bool {
	switch o.kind {
	case kindAttachWrite, kindMessage, kindNewMessage, kindEmbedded:
		return true
	}
	return false
}

// writeStreamInitial returns the property's current bytes to seed a writable
// stream, read from the parent's write buffer (so a re-open sees buffered edits)
// and, for an opened message, falling back to the persisted value. An absent
// property seeds an empty stream.
func (s *Session) writeStreamInitial(parent *object, tag mapi.PropTag) []byte {
	switch parent.kind {
	case kindAttachWrite:
		if v, ok := parent.attachW.pending.Get(tag); ok {
			return streamBytes(tag.Type(), v)
		}
	case kindMessage:
		if v, ok := parent.pendingProps.Get(tag); ok {
			return streamBytes(tag.Type(), v)
		}
		if data, err := s.streamData(parent, tag); err == nil {
			return data
		}
	case kindNewMessage:
		if v, ok := parent.newMsg.props.Get(tag); ok {
			return streamBytes(tag.Type(), v)
		}
	case kindEmbedded:
		if v, ok := parent.embedded.msg.Props.Get(tag); ok {
			return streamBytes(tag.Type(), v)
		}
	}
	return nil
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

// ropWriteStream handles RopWriteStream ([MS-OXCPRPT] 2.2.2.17): it overwrites the
// stream's bytes at the cursor with the request data, growing (zero-extending) the
// buffer when the write runs past the end, then advances the cursor. The data is a
// 16-bit-length-prefixed binary, so a single write carries at most 0xFFFF bytes.
// The bytes are not yet staged to the parent — that happens at CommitStream.
//
// v1 reallocates the whole buffer on each past-end write, so streaming a large
// property in 0xFFFF-byte chunks is O(n^2); acceptable for v1's small streams, but
// a chunked-append/preallocate would be the fix if large stream writes appear.
func (s *Session) ropWriteStream(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	data, e1 := p.BinShort()
	if e1 != nil {
		return false
	}
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.kind != kindStream || obj.stream == nil {
		writeErr(out, ropWriteStream, hindex, ecError)
		return true
	}
	st := obj.stream
	if !st.writable {
		writeErr(out, ropWriteStream, hindex, ecAccessDenied)
		return true
	}
	if end := st.pos + len(data); end > len(st.data) {
		grown := make([]byte, end)
		copy(grown, st.data)
		st.data = grown
	}
	copy(st.data[st.pos:], data)
	st.pos += len(data)
	st.dirty = true

	out.Uint8(ropWriteStream)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(uint16(len(data))) // WrittenSize
	return true
}

// ropCommitStream handles RopCommitStream ([MS-OXCPRPT] 2.2.2.19): it stages a
// writable stream's bytes back into the parent object's write buffer for the
// stream's property, where the parent's SaveChanges then persists it. (hermEX has
// no instance-staging layer, so unlike the reference — where this is a no-op for
// message/attachment streams and the flush happens at save — the commit is the
// point the bytes reach the parent.) A read-only stream commits nothing.
func (s *Session) ropCommitStream(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.kind != kindStream || obj.stream == nil {
		writeErr(out, ropCommitStream, hindex, ecError)
		return true
	}
	if st := obj.stream; st.writable {
		if !s.commitStream(st) {
			writeErr(out, ropCommitStream, hindex, ecError)
			return true
		}
	}
	out.Uint8(ropCommitStream)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}

// commitStream stages a writable stream's bytes into the parent's write buffer for
// the stream's property, converting the bytes back to the property's value form.
// An opened message is also marked touched so SaveChangesMessage advances its
// change number. It reports false if the parent is not a writable kind.
func (s *Session) commitStream(st *streamState) bool {
	val := streamValue(st.tag.Type(), st.data)
	switch st.parent.kind {
	case kindAttachWrite:
		st.parent.attachW.pending.Set(st.tag, val)
	case kindMessage:
		st.parent.pendingProps.Set(st.tag, val)
		st.parent.touched = true
	case kindNewMessage:
		st.parent.newMsg.props.Set(st.tag, val)
	case kindEmbedded:
		st.parent.embedded.msg.Props.Set(st.tag, val)
	default:
		return false
	}
	st.dirty = false
	return true
}

// streamValue converts a stream's bytes back to the value form of its property
// type — the inverse of streamBytes: a Unicode string from UTF-16LE, a string8
// from its code-page bytes, binary verbatim.
func streamValue(typ mapi.PropType, data []byte) any {
	switch typ {
	case mapi.PtUnicode:
		u16s := make([]uint16, 0, len(data)/2)
		for i := 0; i+1 < len(data); i += 2 {
			u16s = append(u16s, uint16(data[i])|uint16(data[i+1])<<8)
		}
		return string(utf16.Decode(u16s))
	case mapi.PtString8:
		return string(data)
	default: // PtBinary, PtObject
		return data
	}
}

// ropSeekStream handles RopSeekStream ([MS-OXCPRPT] 2.2.2.18): it moves the stream
// cursor to Offset (a signed count) from the chosen origin and returns the new
// position. Seeking past the end zero-extends a writable stream; a read-only stream
// clamps to the end. A negative resulting position or an unknown origin is refused.
func (s *Session) ropSeekStream(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	origin, e1 := p.Uint8()
	offRaw, e2 := p.Uint64()
	if e1 != nil || e2 != nil {
		return false
	}
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.kind != kindStream || obj.stream == nil {
		writeErr(out, ropSeekStream, hindex, ecError)
		return true
	}
	st := obj.stream
	var base int64
	switch origin {
	case streamSeekSet:
		base = 0
	case streamSeekCur:
		base = int64(st.pos)
	case streamSeekEnd:
		base = int64(len(st.data))
	default:
		writeErr(out, ropSeekStream, hindex, ecInvalidParam)
		return true
	}
	newpos := base + int64(offRaw)
	if newpos < 0 {
		writeErr(out, ropSeekStream, hindex, ecInvalidParam)
		return true
	}
	if newpos > int64(len(st.data)) {
		if st.writable {
			grown := make([]byte, newpos)
			copy(grown, st.data)
			st.data = grown
		} else {
			newpos = int64(len(st.data))
		}
	}
	st.pos = int(newpos)

	out.Uint8(ropSeekStream)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint64(uint64(newpos)) // NewPosition
	return true
}

// ropSetStreamSize handles RopSetStreamSize ([MS-OXCPRPT] 2.2.2.20): it sets the
// stream length, zero-filling on growth and truncating (clamping the cursor) on
// shrink. Only a writable stream may be resized.
func (s *Session) ropSetStreamSize(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	size, e1 := p.Uint64()
	if e1 != nil {
		return false
	}
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.kind != kindStream || obj.stream == nil {
		writeErr(out, ropSetStreamSize, hindex, ecError)
		return true
	}
	st := obj.stream
	if !st.writable {
		writeErr(out, ropSetStreamSize, hindex, ecAccessDenied)
		return true
	}
	n := int(size)
	if n > len(st.data) {
		grown := make([]byte, n)
		copy(grown, st.data)
		st.data = grown
	} else {
		st.data = st.data[:n]
	}
	if st.pos > n {
		st.pos = n
	}
	st.dirty = true

	out.Uint8(ropSetStreamSize)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}

// ropGetStreamSize handles RopGetStreamSize ([MS-OXCPRPT] 2.2.2.21): it returns the
// stream's current length.
func (s *Session) ropGetStreamSize(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.kind != kindStream || obj.stream == nil {
		writeErr(out, ropGetStreamSize, hindex, ecError)
		return true
	}
	out.Uint8(ropGetStreamSize)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint32(uint32(len(obj.stream.data)))
	return true
}
