package rop

import (
	"errors"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ICS / FastTransfer ROP opcodes ([MS-OXCROPS] 2.2.3). v1 wires the download path
// plus the pure-ROP upload collector imports (hierarchy / deletes / read-state).
const (
	ropFastTransferDestConfigure        uint8 = 0x53
	ropFastTransferDestPutBuffer        uint8 = 0x54
	ropFastTransferSourceGetBuffer      uint8 = 0x4E
	ropSynchronizationConfigure         uint8 = 0x70
	ropSyncImportMessageChange          uint8 = 0x72
	ropSyncImportHierarchyChange        uint8 = 0x73
	ropSyncImportDeletes                uint8 = 0x74
	ropSyncUploadStateStreamBegin       uint8 = 0x75
	ropSyncUploadStateStreamContinue    uint8 = 0x76
	ropSyncUploadStateStreamEnd         uint8 = 0x77
	ropSyncOpenCollector                uint8 = 0x7E
	ropGetLocalReplicaIds               uint8 = 0x7F
	ropSyncImportReadStateChanges       uint8 = 0x80
	ropSyncGetTransferState             uint8 = 0x82
	ropSynchronizationImportMessageMove uint8 = 0x78
)

// fastCopyTo is the FastTransfer destination source-operation for a message-content
// copy (COPYTO) — the only mode the ICS message-body upload uses ([MS-OXCFXICS]
// 2.2.3.1.2.1.1).
const fastCopyTo uint8 = 0x01

// importDeletesTag is the single multivalue-binary property an ImportDeletes
// request carries — PROP_TAG(PT_MV_BINARY, 0) — whose values are the 22-byte
// source keys to delete ([MS-OXCFXICS] 2.2.3.2.4.5).
const importDeletesTag uint32 = 0x00001102

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
	// A contents download bulk-reads the folder's items, bypassing per-message open,
	// so a delegate needs ReadAny; a hierarchy download reads only subfolder
	// metadata, covered by the Visible the folder was opened with. Owner is
	// unrestricted.
	if syncType == objectstore.SyncTypeContents {
		if ok, err := s.authorize(folder.store, folder.folderID, mapi.FrightsReadAny); err != nil {
			writeErr(out, ropSynchronizationConfigure, ohindex, ecError)
			return true
		} else if !ok {
			writeErr(out, ropSynchronizationConfigure, ohindex, ecAccessDenied)
			return true
		}
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
func (s *Session) ropSyncUploadStateStreamEnd(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
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

// ropSyncOpenCollector handles RopSynchronizationOpenCollector ([MS-OXCFXICS]
// 2.2.3.2.1.2): it opens an upload collector on a folder for inline imports
// (hierarchy / deletes / read-state). The collector is both the import target and
// the state-stream sink, and its serialized state is read back via GetTransferState.
func (s *Session) ropSyncOpenCollector(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()
	isContent, e2 := p.Uint8()
	if e1 != nil || e2 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropSyncOpenCollector, ohindex, ecError)
		return true
	}
	// An upload collector imports message/hierarchy changes into the folder — a bulk
	// write. A delegate may open one only with EditAny on the folder (this is the
	// upload's write chokepoint; the import ROPs that follow trust the collector).
	if s.denyWrite(out, ropSyncOpenCollector, ohindex, folder.store, folder.folderID, mapi.FrightsEditAny) {
		return true
	}
	var col *objectstore.UploadCollector
	var err error
	if isContent != 0 {
		col, err = folder.store.NewContentUpload(folder.folderID)
	} else {
		col, err = folder.store.NewHierarchyUpload(folder.folderID)
	}
	if err != nil {
		writeErr(out, ropSyncOpenCollector, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{kind: kindSync, store: folder.store, upload: col, stateSink: col})
	setHandle(handles, ohindex, h)
	out.Uint8(ropSyncOpenCollector)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}

// ropSyncImportHierarchyChange handles RopSynchronizationImportHierarchyChange
// ([MS-OXCFXICS] 2.2.3.2.4.2): it creates or updates a folder from the uploaded
// identity and property sets and answers with the resulting folder id.
func (s *Session) ropSyncImportHierarchyChange(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	hichyvals, e1 := p.PropertyValues()
	propvals, e2 := p.PropertyValues()
	if e1 != nil || e2 != nil {
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.upload == nil {
		writeErr(out, ropSyncImportHierarchyChange, hindex, ecError)
		return true
	}
	fid, err := o.upload.ImportHierarchyChange(hichyvals, propvals)
	if err != nil {
		writeErr(out, ropSyncImportHierarchyChange, hindex, ecError)
		return true
	}
	out.Uint8(ropSyncImportHierarchyChange)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint64(uint64(mapi.MakeEIDEx(1, fid)))
	return true
}

// ropSyncImportDeletes handles RopSynchronizationImportDeletes ([MS-OXCFXICS]
// 2.2.3.2.4.5): the request carries one multivalue-binary property whose values
// are the source keys to hard-delete.
func (s *Session) ropSyncImportDeletes(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint8() // ImportDeleteFlags — v1 always hard-deletes
	propvals, e2 := p.PropertyValues()
	if e1 != nil || e2 != nil {
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.upload == nil {
		writeErr(out, ropSyncImportDeletes, hindex, ecError)
		return true
	}
	if len(propvals) != 1 || uint32(propvals[0].Tag) != importDeletesTag {
		writeErr(out, ropSyncImportDeletes, hindex, ecError)
		return true
	}
	keys, ok := propvals[0].Value.([][]byte)
	if !ok {
		writeErr(out, ropSyncImportDeletes, hindex, ecError)
		return true
	}
	if _, err := o.upload.ImportDeletes(keys); err != nil {
		writeErr(out, ropSyncImportDeletes, hindex, ecError)
		return true
	}
	writeStatusHead(out, ropSyncImportDeletes, hindex)
	return true
}

// ropSyncImportReadStateChanges handles RopSynchronizationImportReadStateChanges
// ([MS-OXCFXICS] 2.2.3.2.4.6): a length-prefixed block of {source key, read flag}
// records. Each record is a u16-length-prefixed source key followed by a one-byte
// read flag.
func (s *Session) ropSyncImportReadStateChanges(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	size, e1 := p.Uint16()
	block, e2 := p.Raw(int(size))
	if e1 != nil || e2 != nil {
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.upload == nil {
		writeErr(out, ropSyncImportReadStateChanges, hindex, ecError)
		return true
	}
	var changes []objectstore.ReadStateChange
	bp := ext.NewPull(block, ext.FlagUTF16)
	for bp.Remaining() > 0 {
		sk, e3 := bp.BinShort()
		mark, e4 := bp.Uint8()
		if e3 != nil || e4 != nil {
			writeErr(out, ropSyncImportReadStateChanges, hindex, ecError)
			return true
		}
		changes = append(changes, objectstore.ReadStateChange{SourceKey: sk, MarkRead: mark != 0})
	}
	if err := o.upload.ImportReadStateChanges(changes); err != nil {
		writeErr(out, ropSyncImportReadStateChanges, hindex, ecError)
		return true
	}
	writeStatusHead(out, ropSyncImportReadStateChanges, hindex)
	return true
}

// ropSyncImportMessageMove handles RopSynchronizationImportMessageMove ([MS-OXCFXICS]
// 2.2.3.2.4.5): the client relocates a message into the contents-upload collector's
// folder under an id it has already assigned in its own replica. The request carries
// five 32-bit-length-prefixed binaries (source folder, source message, predecessor
// change list, destination message, and change number), of which hermEX uses the
// three id XIDs (home-replica 22-byte source keys). The predecessor change list and
// change number are accepted but not compared: hermEX does no PCL conflict detection
// anywhere (its import path is last-writer-wins), so a move always succeeds rather
// than ever returning SYNC_W_CLIENT_CHANGE_NEWER. A source the store no longer holds
// maps to SYNC_E_OBJECT_DELETED. The response message id is zero, because the
// client's destination id is authoritative.
func (s *Session) ropSyncImportMessageMove(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	srcFolderKey, e1 := p.BinEx()
	srcMsgKey, e2 := p.BinEx()
	_, e3 := p.BinEx() // PredecessorChangeList, accepted but not compared (last-writer-wins)
	dstMsgKey, e4 := p.BinEx()
	_, e5 := p.BinEx() // ChangeNumber, the server allocates its own
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.upload == nil {
		writeErr(out, ropSynchronizationImportMessageMove, hindex, ecError)
		return true
	}
	if err := o.upload.ImportMessageMove(srcFolderKey, srcMsgKey, dstMsgKey); err != nil {
		if errors.Is(err, objectstore.ErrObjectDeleted) {
			writeErr(out, ropSynchronizationImportMessageMove, hindex, ecSyncObjectDel)
			return true
		}
		writeErr(out, ropSynchronizationImportMessageMove, hindex, ecError)
		return true
	}
	out.Uint8(ropSynchronizationImportMessageMove)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint64(0) // MessageId; the client's destination id is authoritative
	return true
}

// ropSyncGetTransferState handles RopSynchronizationGetTransferState ([MS-OXCFXICS]
// 2.2.3.2.3.1): it serializes the collector's state into a fresh FastTransfer
// source the client drains via RopFastTransferSourceGetBuffer.
func (s *Session) ropSyncGetTransferState(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()
	if e1 != nil {
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.upload == nil {
		writeErr(out, ropSyncGetTransferState, ohindex, ecError)
		return true
	}
	state, err := o.upload.GetTransferState()
	if err != nil {
		writeErr(out, ropSyncGetTransferState, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{kind: kindSync, store: o.store, fastSrc: &transferStateSource{data: state}})
	setHandle(handles, ohindex, h)
	out.Uint8(ropSyncGetTransferState)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}

// ropSyncImportMessageChange handles RopSynchronizationImportMessageChange
// ([MS-OXCFXICS] 2.2.3.2.4.2): it opens an imported message from its four-property
// identity header and returns a message handle whose body is then filled over a
// FastTransfer destination and persisted by RopSaveChangesMessage. The response
// message id is zero — the client reads the server change number off the saved
// message, not from here.
func (s *Session) ropSyncImportMessageChange(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()
	importFlags, e2 := p.Uint8()
	header, e3 := p.PropertyValues()
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.upload == nil {
		writeErr(out, ropSyncImportMessageChange, ohindex, ecError)
		return true
	}
	um, err := o.upload.ImportMessageChange(importFlags, header)
	if err != nil {
		writeErr(out, ropSyncImportMessageChange, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{kind: kindUploadMessage, store: o.store, uploadMsg: um})
	setHandle(handles, ohindex, h)
	out.Uint8(ropSyncImportMessageChange)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint64(0) // MessageId
	return true
}

// ropFastTransferDestConfigure handles RopFastTransferDestinationConfigure
// ([MS-OXCFXICS] 2.2.3.1.2.1): on an imported message it opens a FastTransfer
// destination (COPYTO, message-content mode) whose PutBuffer chunks reconstruct the
// message body. Only the message-content copy used by ICS upload is supported.
func (s *Session) ropFastTransferDestConfigure(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()
	sourceOp, e2 := p.Uint8()
	_, e3 := p.Uint8() // CopyFlags — only MOVE (0x01); the ICS message copy sets none
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.kind != kindUploadMessage || o.uploadMsg == nil {
		writeErr(out, ropFastTransferDestConfigure, ohindex, ecError)
		return true
	}
	if sourceOp != fastCopyTo {
		writeErr(out, ropFastTransferDestConfigure, ohindex, ecNotSupported)
		return true
	}
	mc := objectstore.NewMessageCollector(o.uploadMsg)
	h := s.alloc(&object{kind: kindFastUpload, store: o.store, msgCollector: mc})
	setHandle(handles, ohindex, h)
	out.Uint8(ropFastTransferDestConfigure)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}

// ropFastTransferDestPutBuffer handles RopFastTransferDestinationPutBuffer
// ([MS-OXCFXICS] 2.2.3.1.2.2): it feeds one chunk of the FastTransfer body into the
// message collector. The destination's transfer status is always zero (it consumes
// the whole chunk); used size echoes the bytes accepted.
func (s *Session) ropFastTransferDestPutBuffer(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	data, e1 := p.BinShort()
	if e1 != nil {
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.msgCollector == nil {
		writeErr(out, ropFastTransferDestPutBuffer, hindex, ecError)
		return true
	}
	if err := o.msgCollector.PutBuffer(data); err != nil {
		writeErr(out, ropFastTransferDestPutBuffer, hindex, ecError)
		return true
	}
	out.Uint8(ropFastTransferDestPutBuffer)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(0)                 // TransferStatus (0 = ready for a destination)
	out.Uint16(0)                 // InProgressCount
	out.Uint16(1)                 // TotalStepCount
	out.Uint8(0)                  // Reserved
	out.Uint16(uint16(len(data))) // UsedSize
	return true
}

// ropGetLocalReplicaIds handles RopGetLocalReplicaIds ([MS-OXCFXICS] 2.2.3.2.1.3):
// it reserves a contiguous block of local ids from the store and answers with the
// home replica GUID and the starting global counter, from which the client mints
// source keys for the new items it uploads.
func (s *Session) ropGetLocalReplicaIds(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	count, e1 := p.Uint32()
	if e1 != nil {
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.store == nil {
		writeErr(out, ropGetLocalReplicaIds, hindex, ecError)
		return true
	}
	begin, replica, err := o.store.AllocateLocalIDs(count)
	if err != nil {
		writeErr(out, ropGetLocalReplicaIds, hindex, ecError)
		return true
	}
	out.Uint8(ropGetLocalReplicaIds)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.GUID(replica)
	gc := mapi.ValueToGC(begin)
	out.Raw(gc[:])
	return true
}

// transferStateSource streams a pre-rendered transfer-state buffer as a FastTransfer
// source. The state is small, so it is held whole rather than produced lazily.
type transferStateSource struct {
	data []byte
	off  int
}

func (b *transferStateSource) GetBuffer(maxLen int) (chunk []byte, last bool, err error) {
	end := min(b.off+maxLen, len(b.data))
	chunk = b.data[b.off:end]
	b.off = end
	return chunk, b.off >= len(b.data), nil
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
