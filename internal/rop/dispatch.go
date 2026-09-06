package rop

import "hermex/internal/ext"

// ROP operation ids ([MS-OXCROPS] 2.2). v1 handles the read-core set.
const (
	ropRelease                     uint8 = 0x01
	ropOpenFolder                  uint8 = 0x02
	ropOpenMessage                 uint8 = 0x03
	ropGetHierarchyTable           uint8 = 0x04
	ropGetContentsTable            uint8 = 0x05
	ropCreateMessage               uint8 = 0x06
	ropGetPropertiesSpecific       uint8 = 0x07
	ropGetPropertiesAll            uint8 = 0x08
	ropSetProperties               uint8 = 0x0A
	ropSaveChangesMessage          uint8 = 0x0C
	ropModifyRecipients            uint8 = 0x0E
	ropReloadCachedInfo            uint8 = 0x10
	ropGetMessageStatus            uint8 = 0x1F
	ropSetMessageStatus            uint8 = 0x20
	ropSubmitMessage               uint8 = 0x32
	ropSetMessageReadFlag          uint8 = 0x11
	ropSetReadFlags                uint8 = 0x66
	ropDeleteMessages              uint8 = 0x1E
	ropMoveCopyMessages            uint8 = 0x33
	ropSetColumns                  uint8 = 0x12
	ropSortTable                   uint8 = 0x13
	ropRestrict                    uint8 = 0x14
	ropQueryRows                   uint8 = 0x15
	ropSeekRow                     uint8 = 0x18
	ropSeekRowBookmark             uint8 = 0x19
	ropCreateBookmark              uint8 = 0x1B
	ropFindRow                     uint8 = 0x4F
	ropExpandRow                   uint8 = 0x59
	ropCollapseRow                 uint8 = 0x5A
	ropSetCollapseState            uint8 = 0x6C
	ropGetCollapseState            uint8 = 0x6B
	ropGetStatus                   uint8 = 0x16
	ropQueryPosition               uint8 = 0x17
	ropSeekRowFractional           uint8 = 0x1A
	ropQueryColumnsAll             uint8 = 0x37
	ropAbort                       uint8 = 0x38
	ropFreeBookmark                uint8 = 0x89
	ropResetTable                  uint8 = 0x81
	ropGetAttachmentTable          uint8 = 0x21
	ropOpenAttachment              uint8 = 0x22
	ropCreateAttachment            uint8 = 0x23
	ropDeleteAttachment            uint8 = 0x24
	ropSaveChangesAttachment       uint8 = 0x25
	ropOpenEmbeddedMessage         uint8 = 0x46
	ropOpenStream                  uint8 = 0x2B
	ropReadStream                  uint8 = 0x2C
	ropWriteStream                 uint8 = 0x2D
	ropSeekStream                  uint8 = 0x2E
	ropSetStreamSize               uint8 = 0x2F
	ropCommitStream                uint8 = 0x5D
	ropGetStreamSize               uint8 = 0x5E
	ropLogon                       uint8 = 0xFE
	ropCreateFolder                uint8 = 0x1C
	ropDeleteFolder                uint8 = 0x1D
	ropMoveFolder                  uint8 = 0x35
	ropCopyFolder                  uint8 = 0x36
	ropEmptyFolder                 uint8 = 0x58
	ropHardDeleteMessages          uint8 = 0x91
	ropHardDelMsgsAndSubfolders    uint8 = 0x92
	ropSetSearchCriteria           uint8 = 0x30
	ropGetSearchCriteria           uint8 = 0x31
	ropDeleteProperties            uint8 = 0x0B
	ropDeletePropertiesNoReplicate uint8 = 0x7A
	ropGetNamesFromPropertyIds     uint8 = 0x55
	ropGetPropertyIdsFromNames     uint8 = 0x56
	ropCopyTo                      uint8 = 0x39
	ropCopyProperties              uint8 = 0x67
	ropSetReceiveFolder            uint8 = 0x26
	ropGetReceiveFolder            uint8 = 0x27
	ropGetReceiveFolderTable       uint8 = 0x68
	ropGetStoreState               uint8 = 0x7B
	ropLongTermIdFromId            uint8 = 0x43
	ropIdFromLongTermId            uint8 = 0x44
	ropRegisterNotification        uint8 = 0x29
	ropNotify                      uint8 = 0x2A
	ropPending                     uint8 = 0x6E
	ropGetPermissionsTable         uint8 = 0x3E
	ropModifyPermissions           uint8 = 0x40
	ropGetRulesTable               uint8 = 0x3F
	ropModifyRules                 uint8 = 0x41
	ropSetSpooler                  uint8 = 0x47
	ropTransportSend               uint8 = 0x4A
	ropGetTransportFolder          uint8 = 0x6D
)

// MAPI return codes ([MS-OXCDATA] 2.4.1) carried in a ROP response ReturnValue.
const (
	ecSuccess         uint32 = 0x00000000
	ecError           uint32 = 0x80004005 // generic failure / unimplemented ROP
	ecNotFound        uint32 = 0x8004010F // MAPI_E_NOT_FOUND (no such folder/object)
	ecNotSupported    uint32 = 0x80040102 // MAPI_E_NO_SUPPORT (unsupported request)
	ecAccessDenied    uint32 = 0x80070005 // MAPI_E_NO_ACCESS (e.g. setting the in-conflict status bit)
	ecInvalidParam    uint32 = 0x80070057 // MAPI_E_INVALID_PARAMETER (e.g. a bad stream-seek origin)
	ecDstNullObject   uint32 = 0x00000503 // a copy's destination handle resolves to no object
	ecDeclineCopy     uint32 = 0x80040306 // MAPI_E_DECLINE_COPY (copy between mismatched object types)
	ecFolderCycle     uint32 = 0x8004060B // MAPI_E_FOLDER_CYCLE (folder copied into its own subtree)
	ecNotImplemented  uint32 = 0x80040FFF // ecNotImplemented (RopGetStoreState, as Exchange 2010+)
	ecUnableToAbort   uint32 = 0x80040114 // MAPI_E_UNABLE_TO_ABORT (RopAbort: nothing async to abort)
	ecInvalidBookmark uint32 = 0x80040405 // MAPI_E_INVALID_BOOKMARK (RopSeekRowFractional: zero denominator)
	ecSyncObjectDel   uint32 = 0x80040800 // SYNC_E_OBJECT_DELETED (ICS move source no longer exists)
)

// ropHandler processes one ROP: it reads the request body from p, appends the
// response to out, and may rebind a server-handle-table slot. It returns false
// when the request could not be parsed, which ends the batch, because a ROP list
// carries no per-ROP length and the reader is then at an unknown offset.
type ropHandler func(s *Session, p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool

// ropTable is the single source of ROP dispatch: an opcode present here is
// answered by its handler, and one absent falls through to the generic error.
// Adding a ROP is one entry plus its handler.
var ropTable = map[uint8]ropHandler{
	ropRelease:                          releaseHandler,
	ropLogon:                            (*Session).ropLogon,
	ropOpenFolder:                       (*Session).ropOpenFolder,
	ropOpenMessage:                      (*Session).ropOpenMessage,
	ropGetPropertiesSpecific:            (*Session).ropGetPropertiesSpecific,
	ropGetPropertiesAll:                 (*Session).ropGetPropertiesAll,
	ropCreateMessage:                    (*Session).ropCreateMessage,
	ropSetProperties:                    (*Session).ropSetProperties,
	ropDeleteProperties:                 (*Session).ropDeleteProperties,
	ropDeletePropertiesNoReplicate:      (*Session).ropDeletePropertiesNoReplicate,
	ropGetPropertyIdsFromNames:          (*Session).ropGetPropertyIdsFromNames,
	ropGetNamesFromPropertyIds:          (*Session).ropGetNamesFromPropertyIds,
	ropCopyProperties:                   (*Session).ropCopyProperties,
	ropCopyTo:                           (*Session).ropCopyTo,
	ropGetReceiveFolder:                 (*Session).ropGetReceiveFolder,
	ropLongTermIdFromId:                 (*Session).ropLongTermIdFromId,
	ropIdFromLongTermId:                 (*Session).ropIdFromLongTermId,
	ropSetReceiveFolder:                 (*Session).ropSetReceiveFolder,
	ropGetReceiveFolderTable:            (*Session).ropGetReceiveFolderTable,
	ropGetStoreState:                    (*Session).ropGetStoreState,
	ropRegisterNotification:             (*Session).ropRegisterNotification,
	ropModifyRecipients:                 (*Session).ropModifyRecipients,
	ropReloadCachedInfo:                 (*Session).ropReloadCachedInformation,
	ropGetMessageStatus:                 (*Session).ropGetMessageStatus,
	ropSetMessageStatus:                 (*Session).ropSetMessageStatus,
	ropSubmitMessage:                    (*Session).ropSubmitMessage,
	ropSetMessageReadFlag:               (*Session).ropSetMessageReadFlag,
	ropSetReadFlags:                     (*Session).ropSetReadFlags,
	ropDeleteMessages:                   (*Session).ropDeleteMessages,
	ropMoveCopyMessages:                 (*Session).ropMoveCopyMessages,
	ropCreateFolder:                     (*Session).ropCreateFolder,
	ropDeleteFolder:                     (*Session).ropDeleteFolder,
	ropMoveFolder:                       (*Session).ropMoveFolder,
	ropCopyFolder:                       (*Session).ropCopyFolder,
	ropEmptyFolder:                      (*Session).ropEmptyFolder,
	ropHardDeleteMessages:               (*Session).ropHardDeleteMessages,
	ropHardDelMsgsAndSubfolders:         (*Session).ropHardDeleteMessagesAndSubfolders,
	ropSetSearchCriteria:                (*Session).ropSetSearchCriteria,
	ropGetSearchCriteria:                (*Session).ropGetSearchCriteria,
	ropSaveChangesMessage:               (*Session).ropSaveChangesMessage,
	ropGetAttachmentTable:               (*Session).ropGetAttachmentTable,
	ropOpenAttachment:                   (*Session).ropOpenAttachment,
	ropOpenEmbeddedMessage:              (*Session).ropOpenEmbeddedMessage,
	ropCreateAttachment:                 (*Session).ropCreateAttachment,
	ropSaveChangesAttachment:            (*Session).ropSaveChangesAttachment,
	ropDeleteAttachment:                 (*Session).ropDeleteAttachment,
	ropGetContentsTable:                 (*Session).ropGetContentsTable,
	ropGetPermissionsTable:              (*Session).ropGetPermissionsTable,
	ropModifyPermissions:                (*Session).ropModifyPermissions,
	ropGetRulesTable:                    (*Session).ropGetRulesTable,
	ropModifyRules:                      (*Session).ropModifyRules,
	ropSetSpooler:                       (*Session).ropSetSpooler,
	ropGetTransportFolder:               (*Session).ropGetTransportFolder,
	ropTransportSend:                    (*Session).ropTransportSend,
	ropSetColumns:                       (*Session).ropSetColumns,
	ropGetHierarchyTable:                (*Session).ropGetHierarchyTable,
	ropSortTable:                        (*Session).ropSortTable,
	ropRestrict:                         (*Session).ropRestrict,
	ropQueryRows:                        (*Session).ropQueryRows,
	ropSeekRow:                          (*Session).ropSeekRow,
	ropGetStatus:                        (*Session).ropGetStatus,
	ropQueryPosition:                    (*Session).ropQueryPosition,
	ropSeekRowFractional:                (*Session).ropSeekRowFractional,
	ropQueryColumnsAll:                  (*Session).ropQueryColumnsAll,
	ropAbort:                            (*Session).ropAbort,
	ropGetCollapseState:                 (*Session).ropGetCollapseState,
	ropFreeBookmark:                     (*Session).ropFreeBookmark,
	ropSeekRowBookmark:                  (*Session).ropSeekRowBookmark,
	ropCreateBookmark:                   (*Session).ropCreateBookmark,
	ropFindRow:                          (*Session).ropFindRow,
	ropExpandRow:                        (*Session).ropExpandRow,
	ropCollapseRow:                      (*Session).ropCollapseRow,
	ropSetCollapseState:                 (*Session).ropSetCollapseState,
	ropResetTable:                       (*Session).ropResetTable,
	ropOpenStream:                       (*Session).ropOpenStream,
	ropReadStream:                       (*Session).ropReadStream,
	ropWriteStream:                      (*Session).ropWriteStream,
	ropCommitStream:                     (*Session).ropCommitStream,
	ropSeekStream:                       (*Session).ropSeekStream,
	ropSetStreamSize:                    (*Session).ropSetStreamSize,
	ropGetStreamSize:                    (*Session).ropGetStreamSize,
	ropSynchronizationConfigure:         (*Session).ropSynchronizationConfigure,
	ropSyncUploadStateStreamBegin:       (*Session).ropSyncUploadStateStreamBegin,
	ropSyncUploadStateStreamContinue:    (*Session).ropSyncUploadStateStreamContinue,
	ropSyncUploadStateStreamEnd:         (*Session).ropSyncUploadStateStreamEnd,
	ropFastTransferSourceGetBuffer:      (*Session).ropFastTransferSourceGetBuffer,
	ropSyncOpenCollector:                (*Session).ropSyncOpenCollector,
	ropSyncImportMessageChange:          (*Session).ropSyncImportMessageChange,
	ropFastTransferDestConfigure:        (*Session).ropFastTransferDestConfigure,
	ropFastTransferDestPutBuffer:        (*Session).ropFastTransferDestPutBuffer,
	ropSyncImportHierarchyChange:        (*Session).ropSyncImportHierarchyChange,
	ropSyncImportDeletes:                (*Session).ropSyncImportDeletes,
	ropSyncImportReadStateChanges:       (*Session).ropSyncImportReadStateChanges,
	ropSyncGetTransferState:             (*Session).ropSyncGetTransferState,
	ropGetLocalReplicaIds:               (*Session).ropGetLocalReplicaIds,
	ropSetLocalReplicaMidsetDeleted:     (*Session).ropSetLocalReplicaMidsetDeleted,
	ropSynchronizationImportMessageMove: (*Session).ropSyncImportMessageMove,
	ropGetPerUserLongTermIds:            (*Session).ropGetPerUserLongTermIds,
	ropGetPerUserGuid:                   (*Session).ropGetPerUserGuid,
	ropReadPerUserInformation:           (*Session).ropReadPerUserInformation,
	ropWritePerUserInformation:          (*Session).ropWritePerUserInformation,
	ropFastTransferSourceCopyMessages:   (*Session).ropFastTransferSourceCopyMessages,
	ropFastTransferSourceCopyFolder:     (*Session).ropFastTransferSourceCopyFolder,
	ropFastTransferSourceCopyTo:         (*Session).ropFastTransferSourceCopyTo,
	ropFastTransferSourceCopyProperties: (*Session).ropFastTransferSourceCopyProperties,
	ropProgress:                         (*Session).ropProgress,
}

// releaseHandler adapts RopRelease to the table's signature. It is the one ROP
// that reads no request body and writes no response, so it can never end the
// batch.
func releaseHandler(s *Session, _ *ext.Pull, _ *ext.Push, handles []uint32, hindex uint8) bool {
	s.ropRelease(handles, hindex)
	return true
}

// Dispatch parses the request ROP list and returns the response ROP bytes plus
// the updated server-handle table, which the RopBuffer codec re-frames. Each
// ROP resolves its handle slot against the table, mutates the session's object
// graph, and appends its response.
//
// ROPs are variable-length with no per-ROP length prefix, so an unrecognized
// ROP cannot be skipped: dispatch emits the 6-byte generic error for it and
// stops, since the remaining ROPs in the batch can no longer be located. A
// short or truncated header likewise ends the batch.
func (s *Session) Dispatch(ropList []byte, reqHandles []uint32) (respRops []byte, respHandles []uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	handles := append([]uint32(nil), reqHandles...)
	p := ext.NewPull(ropList, ext.FlagUTF16)
	out := ext.NewPush(ext.FlagUTF16)
	for p.Remaining() > 0 {
		ropID, hindex, ok := pullRopHeader(p)
		if !ok {
			break
		}
		run, known := ropTable[ropID]
		if !known {
			writeErr(out, ropID, hindex, ecError)
			break
		}
		if !run(s, p, out, handles, hindex) {
			break
		}
	}
	// After the ROP batch, drain notifications into the same response, mirroring
	// the reference's end-of-Execute notify drain. This runs on every Execute,
	// including an empty one (a wake-up Execute that exists only to collect pending
	// notifications), and is a no-op when the session has no subscriptions.
	s.poll(out)
	return out.Bytes(), handles
}

// pullRopHeader reads the three-byte ROP header: RopId, LogonId (a single logon
// in v1, so it is discarded) and the handle index. ok is false when the list
// ends mid-header, which ends the batch with no response.
func pullRopHeader(p *ext.Pull) (ropID, hindex uint8, ok bool) {
	ropID, e1 := p.Uint8()
	_, e2 := p.Uint8() // LogonId (a single logon in v1)
	hindex, e3 := p.Uint8()
	return ropID, hindex, e1 == nil && e2 == nil && e3 == nil
}

// writeErr appends the 6-byte generic ROP error response: RopId, HandleIndex, ec.
func writeErr(out *ext.Push, ropID, hindex uint8, ec uint32) {
	out.Uint8(ropID)
	out.Uint8(hindex)
	out.Uint32(ec)
}

// handleAt reads a server-handle-table slot, returning the null handle when the
// index is out of range.
func handleAt(handles []uint32, idx uint8) uint32 {
	if int(idx) < len(handles) {
		return handles[idx]
	}
	return 0xFFFFFFFF
}

// setHandle writes a server-handle-table slot when the index is in range.
func setHandle(handles []uint32, idx uint8, h uint32) {
	if int(idx) < len(handles) {
		handles[idx] = h
	}
}
