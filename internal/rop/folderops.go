package rop

import (
	"errors"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ropCreateFolder handles RopCreateFolder ([MS-OXCFOLD] 2.2.1.1): it creates a new
// subfolder under the folder identified by the input handle. openExisting and folder
// comment are parsed but not yet acted on (v1 always creates, never reopens).
func (s *Session) ropCreateFolder(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_ /* ohindex */, eh := p.Uint8() // output handle index (v1 does not allocate)
	_ /* ft */, e0 := p.Uint8()      // FolderType
	uv, e1 := p.Uint8()              // UseUnicode
	_ /* oe */, e2 := p.Uint8()      // OpenExisting
	_ /* rs */, e3 := p.Uint32()     // Reserved
	if eh != nil || e0 != nil || e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	var name, comment string
	if uv != 0 {
		n, e5 := p.Unicode()
		c, e6 := p.Unicode()
		if e5 != nil || e6 != nil {
			return false
		}
		name, comment = n, c
	} else {
		n, e5 := p.String8()
		c, e6 := p.String8()
		if e5 != nil || e6 != nil {
			return false
		}
		name, comment = n, c
	}
	// Use comment string but v1 drops it (store doesn't store folder comments yet)
	_ = comment

	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropCreateFolder, hindex, ecError)
		return true
	}
	if s.denyWrite(out, ropCreateFolder, hindex, folder.store, folder.folderID, mapi.FrightsCreateSubfolder) {
		return true
	}
	folderID, err := folder.store.CreateFolder(&folder.folderID, name)
	if err != nil {
		writeErr(out, ropCreateFolder, hindex, ecError)
		return true
	}
	out.Uint8(ropCreateFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint64(uint64(mapi.MakeEIDEx(1, uint64(folderID)))) // FolderId (EID, matching RopLogon's encoding)
	out.Uint8(0)                                            // IsExisting
	out.Uint8(0)                                            // HasRules
	out.Uint64(0)                                           // Ghost (unused)
	return true
}

// ropDeleteFolder handles RopDeleteFolder ([MS-OXCFOLD] 2.2.1.2): it deletes the
// folder identified by fid. v1 does only synchronous, single-folder delete.
func (s *Session) ropDeleteFolder(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_ /* flags */, e1 := p.Uint8() // DeleteFlags (e.g. DEL_MESSAGES, DEL_FOLDERS)
	fid, e2 := p.Uint64()          // FolderId
	if e1 != nil || e2 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropDeleteFolder, hindex, ecError)
		return true
	}
	// Deleting a folder requires owner rights on the folder being removed.
	if s.denyWrite(out, ropDeleteFolder, hindex, folder.store, int64(mapi.EID(fid).GCValue()), mapi.FrightsOwner) {
		return true
	}
	if err := folder.store.DeleteFolder(int64(mapi.EID(fid).GCValue())); err != nil {
		writeErr(out, ropDeleteFolder, hindex, ecError)
		return true
	}
	out.Uint8(ropDeleteFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // PartialCompletion
	return true
}

// ropMoveFolder handles RopMoveFolder ([MS-OXCFOLD] 2.2.1.3): it moves/renames a
// folder by changing its parent and/or display name. v1 always synchronous.
func (s *Session) ropMoveFolder(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	dhindex, e1 := p.Uint8() // DestHandleIndex (handle to the new parent folder)
	_ /* wantAsync */, e2 := p.Uint8()
	uv, e3 := p.Uint8() // UseUnicode
	fid, e4 := p.Uint64()
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	var newName string
	if uv != 0 {
		n, e5 := p.Unicode()
		if e5 != nil {
			return false
		}
		newName = n
	} else {
		n, e5 := p.String8()
		if e5 != nil {
			return false
		}
		newName = n
	}

	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropMoveFolder, hindex, ecError)
		return true
	}
	movedFID := int64(mapi.EID(fid).GCValue())
	// Moving (or renaming) a folder modifies the folder itself: it requires owner
	// rights on the folder being moved — the same right RopDeleteFolder requires to
	// remove one. For an owner this short-circuits.
	if s.denyWrite(out, ropMoveFolder, hindex, folder.store, movedFID, mapi.FrightsOwner) {
		return true
	}
	dest := s.get(handleAt(handles, dhindex))
	var newParent *int64
	if dest != nil && dest.kind == kindFolder {
		// A reparent files the folder under a new parent. RenameFolder runs through
		// the source store, so the new parent must be the same physical mailbox (a
		// cross-mailbox reparent would collide on well-known ids); the caller then
		// needs CreateSubfolder on that parent.
		if dest.store == nil || folder.store.Dir() != dest.store.Dir() {
			writeErr(out, ropMoveFolder, hindex, ecNotSupported)
			return true
		}
		if s.denyWrite(out, ropMoveFolder, hindex, dest.store, dest.folderID, mapi.FrightsCreateSubfolder) {
			return true
		}
		newParent = &dest.folderID
	}
	if err := folder.store.RenameFolder(movedFID, newParent, newName); err != nil {
		writeErr(out, ropMoveFolder, hindex, ecError)
		return true
	}
	out.Uint8(ropMoveFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // PartialCompletion
	return true
}

// ropCopyFolder handles RopCopyFolder ([MS-OXCFOLD] 2.2.1.4): it copies the folder
// identified by fid (with its messages, and — when WantRecursive is set — its
// subfolders) under the destination folder at DestHandleIndex, with the supplied
// new name. Copying a folder into its own subtree is refused with
// MAPI_E_FOLDER_CYCLE. v1 is always synchronous.
func (s *Session) ropCopyFolder(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	dhindex, e0 := p.Uint8()
	_ /* wantAsync */, e1 := p.Uint8()
	wantRecursive, e2 := p.Uint8()
	uv, e3 := p.Uint8()
	fid, e4 := p.Uint64()
	if e0 != nil || e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	var newName string
	if uv != 0 {
		n, e5 := p.Unicode()
		if e5 != nil {
			return false
		}
		newName = n
	} else {
		n, e5 := p.String8()
		if e5 != nil {
			return false
		}
		newName = n
	}

	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropCopyFolder, hindex, ecError)
		return true
	}
	copiedFID := int64(mapi.EID(fid).GCValue())
	// Copying a folder reads its contents: it requires ReadAny on the folder being
	// copied (denyWrite gates an arbitrary right, not only writes). For an owner the
	// authorize check short-circuits.
	if s.denyWrite(out, ropCopyFolder, hindex, folder.store, copiedFID, mapi.FrightsReadAny) {
		return true
	}
	dest := s.get(handleAt(handles, dhindex))
	if dest == nil || dest.kind != kindFolder || dest.store == nil {
		writeErr(out, ropCopyFolder, hindex, ecError)
		return true
	}
	// CopyFolder runs through the source store, so the copy lands under a parent in
	// the same physical mailbox (a cross-mailbox copy would collide on well-known
	// ids); creating the new subfolder there needs CreateSubfolder.
	if folder.store.Dir() != dest.store.Dir() {
		writeErr(out, ropCopyFolder, hindex, ecNotSupported)
		return true
	}
	if s.denyWrite(out, ropCopyFolder, hindex, dest.store, dest.folderID, mapi.FrightsCreateSubfolder) {
		return true
	}
	if _, err := folder.store.CopyFolder(copiedFID, dest.folderID, newName, wantRecursive != 0); err != nil {
		switch {
		case errors.Is(err, objectstore.ErrFolderCycle):
			writeErr(out, ropCopyFolder, hindex, ecFolderCycle)
		case errors.Is(err, objectstore.ErrNotFound):
			writeErr(out, ropCopyFolder, hindex, ecNotFound)
		default:
			writeErr(out, ropCopyFolder, hindex, ecError)
		}
		return true
	}
	out.Uint8(ropCopyFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // PartialCompletion
	return true
}

// ropHardDeleteMessagesAndSubfolders handles RopHardDeleteMessagesAndSubfolders
// ([MS-OXCFOLD] 2.2.1.10 / [MS-OXCROPS] 2.2.4.10). Its request wire and response are
// identical to RopEmptyFolder (WantAsynchronous, WantDeleteAssociated -> a
// PartialCompletion), but besides clearing the folder's messages it ALSO removes the
// folder's subfolders, each with its whole subtree. v1 routes message removal through
// the dumpster (the same soft-delete the existing hard-delete ROPs use, since an
// Exchange hard delete still lands in Recoverable Items); a failure to remove any
// message or subfolder sets PartialCompletion.
func (s *Session) ropHardDeleteMessagesAndSubfolders(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_ /* wantAsync */, e1 := p.Uint8()
	_ /* wantDeleteAssociated */, e2 := p.Uint8()
	if e1 != nil || e2 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropHardDelMsgsAndSubfolders, hindex, ecError)
		return true
	}
	// Clearing a folder and dropping its subfolders deletes items: it requires
	// DeleteAny on the folder, like RopEmptyFolder.
	if s.denyWrite(out, ropHardDelMsgsAndSubfolders, hindex, folder.store, folder.folderID, mapi.FrightsDeleteAny) {
		return true
	}
	var partial uint8
	msgs, err := folder.store.ListMessages(folder.folderID)
	if err != nil {
		writeErr(out, ropHardDelMsgsAndSubfolders, hindex, ecError)
		return true
	}
	for _, m := range msgs {
		if err := folder.store.SoftDeleteObject(m.ID); err != nil {
			partial = 1
		}
	}
	children, err := childFolders(folder.store, folder.folderID)
	if err != nil {
		writeErr(out, ropHardDelMsgsAndSubfolders, hindex, ecError)
		return true
	}
	for _, c := range children {
		if err := folder.store.DeleteFolder(c.ID); err != nil {
			partial = 1
		}
	}
	out.Uint8(ropHardDelMsgsAndSubfolders)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(partial) // PartialCompletion
	return true
}

// ropEmptyFolder handles RopEmptyFolder ([MS-OXCFOLD] 2.2.1.5): it soft-deletes all
// messages (and optionally FAI/associated items) from the folder into the
// Recoverable Items dumpster (recoverable until retention). v1 always synchronous,
// does not yet honour wantDeleteAssociated.
func (s *Session) ropEmptyFolder(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_ /* wantAsync */, e1 := p.Uint8()
	_ /* wantDeleteAssociated */, e2 := p.Uint8()
	if e1 != nil || e2 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropEmptyFolder, hindex, ecError)
		return true
	}
	// Emptying a folder deletes its items: it requires DeleteAny on the folder.
	if s.denyWrite(out, ropEmptyFolder, hindex, folder.store, folder.folderID, mapi.FrightsDeleteAny) {
		return true
	}
	msgs, err := folder.store.ListMessages(folder.folderID)
	if err != nil {
		writeErr(out, ropEmptyFolder, hindex, ecError)
		return true
	}
	for _, m := range msgs {
		if err := folder.store.SoftDeleteObject(m.ID); err != nil {
			writeErr(out, ropEmptyFolder, hindex, ecError)
			return true
		}
	}
	out.Uint8(ropEmptyFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // PartialCompletion
	return true
}

// ropHardDeleteMessages handles RopHardDeleteMessages ([MS-OXCSTOR] 2.2.1.1).
// Under the Recoverable Items model every delete stays recoverable, so it routes
// the messages to the dumpster (soft delete) rather than purging them; a true purge
// happens only via retention or an explicit dumpster purge.
func (s *Session) ropHardDeleteMessages(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_ /* wantAsync */, e1 := p.Uint8()
	_ /* notifyNonRead */, e2 := p.Uint8()
	mids, e3 := p.BinShort() // MessageIds (binary blob)
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropHardDeleteMessages, hindex, ecError)
		return true
	}
	// Deleting messages from the folder requires DeleteAny.
	if s.denyWrite(out, ropHardDeleteMessages, hindex, folder.store, folder.folderID, mapi.FrightsDeleteAny) {
		return true
	}
	// MessageIds is a flat sequence of 8-byte little-endian message EIDs; the store
	// row is the EID's global-counter value (the same extraction RopMoveCopyMessages
	// uses), not the raw EID.
	for i := 0; i+8 <= len(mids); i += 8 {
		eid := uint64(mids[i]) | uint64(mids[i+1])<<8 |
			uint64(mids[i+2])<<16 | uint64(mids[i+3])<<24 |
			uint64(mids[i+4])<<32 | uint64(mids[i+5])<<40 |
			uint64(mids[i+6])<<48 | uint64(mids[i+7])<<56
		mid := int64(mapi.EID(eid).GCValue())
		if err := folder.store.SoftDeleteObject(mid); err != nil {
			writeErr(out, ropHardDeleteMessages, hindex, ecError)
			return true
		}
	}
	out.Uint8(ropHardDeleteMessages)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // PartialCompletion
	return true
}

// ropSetSearchCriteria handles RopSetSearchCriteria ([MS-OXCFOLD] 2.2.1.4): it sets
// the restriction, scope folders, and search flags on a search folder. v1 has no
// search-folder backend, so the request body is fully consumed (to keep the parser
// aligned in a multi-ROP batch) and ecNotSupported is returned. The body is
// RestrictionDataSize (u16) + RestrictionData + FolderIds (EID_ARRAY) + SearchFlags (u32).
func (s *Session) ropSetSearchCriteria(p *ext.Pull, out *ext.Push, _ []uint32, hindex uint8) bool {
	resSize, e1 := p.Uint16() // RestrictionDataSize
	if e1 != nil {
		return false
	}
	if _, err := p.Raw(int(resSize)); err != nil { // RestrictionData
		return false
	}
	if _, err := p.EIDs(); err != nil { // FolderIds (EID_ARRAY, wide-count)
		return false
	}
	if _, err := p.Uint32(); err != nil { // SearchFlags
		return false
	}
	writeErr(out, ropSetSearchCriteria, hindex, ecNotSupported)
	return true
}

// ropGetSearchCriteria handles RopGetSearchCriteria ([MS-OXCFOLD] 2.2.1.5): it
// returns the restriction, scope folders, and search status of a search folder.
// v1 has no search-folder backend, so the request body (three u8 flags) is
// consumed and ecNotSupported is returned.
func (s *Session) ropGetSearchCriteria(p *ext.Pull, out *ext.Push, _ []uint32, hindex uint8) bool {
	_ /* useUnicode */, e1 := p.Uint8()
	_ /* includeRestriction */, e2 := p.Uint8()
	_ /* includeFolders */, e3 := p.Uint8()
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	writeErr(out, ropGetSearchCriteria, hindex, ecNotSupported)
	return true
}

// ropMoveCopyMessages handles RopMoveCopyMessages ([MS-OXCFOLD] 2.2.1.6): it moves
// or copies messages between folders. Already present in msgops.go; this comment
// marks dispatch recognition of the handler for the folder-ops file grouping.
