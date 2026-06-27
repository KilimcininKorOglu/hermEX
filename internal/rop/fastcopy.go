package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// FastTransfer generic-copy source ROP opcodes ([MS-OXCROPS] 2.2.13). Each opens a
// download stream (a CopyContext) on a source object; the client then drains it
// through RopFastTransferSourceGetBuffer.
const ropFastTransferSourceCopyTo uint8 = 0x4D

// ropFastTransferSourceCopyTo handles RopFastTransferSourceCopyTo ([MS-OXCFXICS]
// 2.2.3.1.1.1 / 2.2.4.1.2): it opens a generic-copy download of every property of
// the source object except the excluded set, together with its sub-objects, as a
// messageContent. The new download handle is placed in the output slot for the
// client to drain through RopFastTransferSourceGetBuffer.
//
// v1 serves a stored-message source (the common case for a FastTransfer download);
// a folder or attachment source is refused (a documented deferral). The Level byte
// (embedded-message recursion depth) and SendOptions (the stream codec is always
// UTF-16) are parsed but not honored.
func (s *Session) ropFastTransferSourceCopyTo(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()
	_, e2 := p.Uint8()  // Level — embedded-message recursion depth, flat in v1
	_, e3 := p.Uint32() // CopyFlags — not applicable to a read-only download source
	_, e4 := p.Uint8()  // SendOptions — the stream codec is always UTF-16
	excluded, e5 := p.PropTags()
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return false
	}
	src := s.get(handleAt(handles, hindex))
	if src == nil || src.kind != kindMessage || src.store == nil {
		writeErr(out, ropFastTransferSourceCopyTo, ohindex, ecError)
		return true
	}
	// A FastTransfer download bulk-reads the message, bypassing per-property open, so
	// it gates like a read of the message: ReadAny on its parent folder (an owner is
	// unrestricted).
	if ok, err := s.authorize(src.store, src.folderID, mapi.FrightsReadAny); err != nil {
		writeErr(out, ropFastTransferSourceCopyTo, ohindex, ecError)
		return true
	} else if !ok {
		writeErr(out, ropFastTransferSourceCopyTo, ohindex, ecAccessDenied)
		return true
	}
	col, err := src.store.NewCopyToMessageSource(src.messageID, excluded)
	if err != nil {
		writeErr(out, ropFastTransferSourceCopyTo, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{kind: kindSync, store: src.store, fastSrc: col})
	setHandle(handles, ohindex, h)
	out.Uint8(ropFastTransferSourceCopyTo)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}
