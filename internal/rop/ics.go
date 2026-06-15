package rop

import (
	"hermex/internal/ext"
	"hermex/internal/objectstore"
)

// ICS / FastTransfer ROP opcodes ([MS-OXCROPS] 2.2.3). v1 wires the download path.
const (
	ropFastTransferSourceGetBuffer   uint8 = 0x4E
	ropSynchronizationConfigure      uint8 = 0x70
	ropSyncUploadStateStreamBegin    uint8 = 0x75
	ropSyncUploadStateStreamContinue uint8 = 0x76
	ropSyncUploadStateStreamEnd      uint8 = 0x77
)

// FastTransfer transfer-status values ([MS-OXCFXICS] 2.2.3.1.1.5.1 / mapi_types).
const (
	transferStatusError   uint16 = 0x0000
	transferStatusPartial uint16 = 0x0001
	transferStatusDone    uint16 = 0x0003
)

// fastChunkCap bounds a single GetBuffer chunk so the u16-length-prefixed transfer
// buffer in the response can never overflow, whatever buffer size the client asks
// for. The 0xBABE sentinel (client defers to the server's max) maps here too.
const fastChunkCap = 0xF000

// fastTransferSource is a server object the client drains via
// RopFastTransferSourceGetBuffer — an ICS download collector in this increment, a
// transfer-state buffer in a later one.
type fastTransferSource interface {
	GetBuffer(maxLen int) (chunk []byte, last bool, err error)
}

// stateStreamSink receives the client's prior synchronization state, replayed as a
// sequence of idset streams before the first transfer ([MS-OXCFXICS] 3.3.5.2).
type stateStreamSink interface {
	BeginStateStream(metaTag uint32) error
	ContinueStateStream(data []byte) error
	EndStateStream() error
}

// ropSynchronizationConfigure handles RopSynchronizationConfigure ([MS-OXCFXICS]
// 2.2.3.2.1.1): it opens a download sync context on a folder handle for the
// requested sync type, flags, and property filter. The restriction is parsed but
// not applied in v1 (a documented limitation, as with RopRestrict). The response
// places the new sync-context handle in the output slot.
func (s *Session) ropSynchronizationConfigure(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()
	syncType, e2 := p.Uint8()
	_, e3 := p.Uint8() // SendOptions — the stream codec always emits UTF-16
	syncFlags, e4 := p.Uint16()
	restrictionSize, e5 := p.Uint16()
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return false
	}
	if restrictionSize != 0 {
		if _, err := p.Raw(int(restrictionSize)); err != nil {
			return false
		}
	}
	extraFlags, e6 := p.Uint32()
	propTags, e7 := p.PropTags()
	if e6 != nil || e7 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropSynchronizationConfigure, ohindex, ecError)
		return true
	}
	var col *objectstore.DownloadCollector
	var err error
	switch syncType {
	case objectstore.SyncTypeContents:
		col, err = folder.store.NewContentDownloadCollector(folder.folderID, syncFlags, extraFlags, propTags)
	case objectstore.SyncTypeHierarchy:
		col, err = folder.store.NewHierarchyDownloadCollector(folder.folderID, syncFlags, propTags)
	default:
		writeErr(out, ropSynchronizationConfigure, ohindex, ecError)
		return true
	}
	if err != nil {
		writeErr(out, ropSynchronizationConfigure, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{kind: kindSync, store: folder.store, fastSrc: col, stateSink: col})
	setHandle(handles, ohindex, h)
	out.Uint8(ropSynchronizationConfigure)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}

// ropSyncUploadStateStreamBegin handles RopSyncUploadStateStreamBegin
// ([MS-OXCFXICS] 2.2.3.2.2.1): it opens a state stream for one idset meta-tag on a
// sync context. BufferSize is the informational total the client will send.
func (s *Session) ropSyncUploadStateStreamBegin(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	stateProp, e1 := p.Uint32()
	_, e2 := p.Uint32() // BufferSize — informational
	if e1 != nil || e2 != nil {
		return false
	}
	sink := s.stateSink(handles, hindex)
	if sink == nil {
		writeErr(out, ropSyncUploadStateStreamBegin, hindex, ecError)
		return true
	}
	if err := sink.BeginStateStream(stateProp); err != nil {
		writeErr(out, ropSyncUploadStateStreamBegin, hindex, ecError)
		return true
	}
	writeStatusHead(out, ropSyncUploadStateStreamBegin, hindex)
	return true
}

// ropSyncUploadStateStreamContinue handles RopSyncUploadStateStreamContinue
// ([MS-OXCFXICS] 2.2.3.2.2.2): it appends a chunk to the open state stream.
func (s *Session) ropSyncUploadStateStreamContinue(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	data, e1 := p.BinEx()
	if e1 != nil {
		return false
	}
	sink := s.stateSink(handles, hindex)
	if sink == nil {
		writeErr(out, ropSyncUploadStateStreamContinue, hindex, ecError)
		return true
	}
	if err := sink.ContinueStateStream(data); err != nil {
		writeErr(out, ropSyncUploadStateStreamContinue, hindex, ecError)
		return true
	}
	writeStatusHead(out, ropSyncUploadStateStreamContinue, hindex)
	return true
}

// ropSyncUploadStateStreamEnd handles RopSyncUploadStateStreamEnd ([MS-OXCFXICS]
// 2.2.3.2.2.3): it folds the buffered idset into the sync context's state.
func (s *Session) ropSyncUploadStateStreamEnd(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	sink := s.stateSink(handles, hindex)
	if sink == nil {
		writeErr(out, ropSyncUploadStateStreamEnd, hindex, ecError)
		return true
	}
	if err := sink.EndStateStream(); err != nil {
		writeErr(out, ropSyncUploadStateStreamEnd, hindex, ecError)
		return true
	}
	writeStatusHead(out, ropSyncUploadStateStreamEnd, hindex)
	return true
}

// ropFastTransferSourceGetBuffer handles RopFastTransferSourceGetBuffer
// ([MS-OXCFXICS] 2.2.3.1.1.5): it streams the next chunk of a FastTransfer source.
// A handle-resolution failure is a ROP error; a streaming failure is reported in
// the body's transfer-status field with an empty buffer, as the protocol prescribes.
func (s *Session) ropFastTransferSourceGetBuffer(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	bufferSize, e1 := p.Uint16()
	if e1 != nil {
		return false
	}
	if bufferSize == 0xBABE {
		if _, e := p.Uint16(); e != nil { // MaximumBufferSize — we cap ourselves
			return false
		}
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.fastSrc == nil {
		writeErr(out, ropFastTransferSourceGetBuffer, hindex, ecError)
		return true
	}
	max := int(bufferSize)
	if bufferSize == 0xBABE || max > fastChunkCap {
		max = fastChunkCap
	}
	chunk, last, err := o.fastSrc.GetBuffer(max)
	status := transferStatusPartial
	switch {
	case err != nil:
		chunk, status = nil, transferStatusError
	case last:
		status = transferStatusDone
	}
	out.Uint8(ropFastTransferSourceGetBuffer)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(status)
	out.Uint16(0)           // InProgressCount
	out.Uint16(0)           // TotalStepCount
	out.Uint8(0)            // Reserved
	_ = out.BinShort(chunk) // chunk <= fastChunkCap < 0xFFFF, so this never errors
	return true
}

// stateSink resolves a sync-context handle to its state-stream sink, or nil.
func (s *Session) stateSink(handles []uint32, hindex uint8) stateStreamSink {
	o := s.get(handleAt(handles, hindex))
	if o == nil {
		return nil
	}
	return o.stateSink
}

// writeStatusHead emits the standard ROP response head (RopId, InputHandleIndex,
// ecSuccess) for a non-handle-producing ROP.
func writeStatusHead(out *ext.Push, ropID, hindex uint8) {
	out.Uint8(ropID)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
}
