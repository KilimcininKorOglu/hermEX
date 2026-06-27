package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// FastTransfer generic-copy source ROP opcodes ([MS-OXCROPS] 2.2.13). Each opens a
// download stream (a CopyContext) on a source object; the client then drains it
// through RopFastTransferSourceGetBuffer.
const (
	ropFastTransferSourceCopyTo         uint8 = 0x4D
	ropFastTransferSourceCopyProperties uint8 = 0x69
)

// ropProgress is the asynchronous-operation status poll ([MS-OXCROPS] 2.2.8.13).
const ropProgress uint8 = 0x50

// ropProgress handles RopProgress ([MS-OXCPRPT] 2.2.2.13): a client polls the status
// of an asynchronous server operation previously started with WantAsynchronous.
// hermEX runs every ROP synchronously and never returns a RopProgress handle for the
// client to poll, so there is no asynchronous operation to report on; the request is
// answered ecNotSupported.
func (s *Session) ropProgress(p *ext.Pull, out *ext.Push, _ []uint32, hindex uint8) bool {
	if _, err := p.Uint8(); err != nil { // WantCancel
		return false
	}
	writeErr(out, ropProgress, hindex, ecNotSupported)
	return true
}

// openCopySource resolves a stored-message source, gates it as a read, builds the
// copy stream through the caller's constructor, and installs the resulting download
// handle in the output slot. It centralizes the source validation, permission gate,
// and response shared by the generic-copy source ROPs.
//
// v1 serves a stored-message source (the common case for a FastTransfer download);
// a folder or attachment source is refused (a documented deferral). A FastTransfer
// download bulk-reads the message, bypassing per-property open, so it gates like a
// read of the message: ReadAny on its parent folder (an owner is unrestricted).
func (s *Session) openCopySource(out *ext.Push, ropID uint8, handles []uint32, hindex, ohindex uint8, build func(*objectstore.Store, int64) (*objectstore.CopyContext, error)) bool {
	src := s.get(handleAt(handles, hindex))
	if src == nil || src.kind != kindMessage || src.store == nil {
		writeErr(out, ropID, ohindex, ecError)
		return true
	}
	if ok, err := s.authorize(src.store, src.folderID, mapi.FrightsReadAny); err != nil {
		writeErr(out, ropID, ohindex, ecError)
		return true
	} else if !ok {
		writeErr(out, ropID, ohindex, ecAccessDenied)
		return true
	}
	col, err := build(src.store, src.messageID)
	if err != nil {
		writeErr(out, ropID, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{kind: kindSync, store: src.store, fastSrc: col})
	setHandle(handles, ohindex, h)
	out.Uint8(ropID)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}

// ropFastTransferSourceCopyTo handles RopFastTransferSourceCopyTo ([MS-OXCFXICS]
// 2.2.3.1.1.1 / 2.2.4.1.2): it opens a generic-copy download of every property of
// the source message except the excluded set, together with its sub-objects, as a
// messageContent. The Level byte (embedded-message recursion depth) and SendOptions
// (the stream codec is always UTF-16) are parsed but not honored.
func (s *Session) ropFastTransferSourceCopyTo(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()
	_, e2 := p.Uint8()  // Level — embedded-message recursion depth, flat in v1
	_, e3 := p.Uint32() // CopyFlags — not applicable to a read-only download source
	_, e4 := p.Uint8()  // SendOptions — the stream codec is always UTF-16
	excluded, e5 := p.PropTags()
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return false
	}
	return s.openCopySource(out, ropFastTransferSourceCopyTo, handles, hindex, ohindex,
		func(st *objectstore.Store, mid int64) (*objectstore.CopyContext, error) {
			return st.NewCopyToMessageSource(mid, excluded)
		})
}

// ropFastTransferSourceCopyProperties handles RopFastTransferSourceCopyProperties
// ([MS-OXCFXICS] 2.2.3.1.1.2 / 2.2.4.1.1): it opens a generic-copy download of only
// the listed properties of the source message, with no sub-objects, as a propList.
// The Level byte and SendOptions are parsed but not honored (as for CopyTo).
func (s *Session) ropFastTransferSourceCopyProperties(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()
	_, e2 := p.Uint8() // Level — embedded-message recursion depth, flat in v1
	_, e3 := p.Uint8() // CopyFlags — not applicable to a read-only download source
	_, e4 := p.Uint8() // SendOptions — the stream codec is always UTF-16
	included, e5 := p.PropTags()
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return false
	}
	return s.openCopySource(out, ropFastTransferSourceCopyProperties, handles, hindex, ohindex,
		func(st *objectstore.Store, mid int64) (*objectstore.CopyContext, error) {
			return st.NewCopyPropertiesMessageSource(mid, included)
		})
}
