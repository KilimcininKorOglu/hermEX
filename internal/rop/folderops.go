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
// pullFolderString reads one string in the encoding the request's UseUnicode
// byte selects. The folder ROPs carry that byte once and every string after it
// follows the same encoding.
func pullFolderString(p *ext.Pull, useUnicode uint8) (string, error) {
	if useUnicode != 0 {
		return p.Unicode()
	}
	return p.String8()
}

// pullCreateFolderRequest reads a RopCreateFolder request and returns the new
// folder's name. The output handle index, folder type, OpenExisting flag and
// reserved field are read to keep the stream framed and then dropped: v1 always
// creates a generic folder and allocates no handle for it. The comment is read
// for the same reason, since the store does not model folder comments yet.
func pullCreateFolderRequest(p *ext.Pull) (name string, ok bool) {
	_ /* ohindex */, eh := p.Uint8() // output handle index (v1 does not allocate)
	_ /* ft */, e0 := p.Uint8()      // FolderType
	uv, e1 := p.Uint8()              // UseUnicode
	_ /* oe */, e2 := p.Uint8()      // OpenExisting
	_ /* rs */, e3 := p.Uint32()     // Reserved
	if eh != nil || e0 != nil || e1 != nil || e2 != nil || e3 != nil {
		return "", false
	}
	name, e5 := pullFolderString(p, uv)
	_ /* comment */, e6 := pullFolderString(p, uv)
	return name, e5 == nil && e6 == nil
}

func (s *Session) ropCreateFolder(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	name, framed := pullCreateFolderRequest(p)
	if !framed {
		return false
	}
	folder, ok := s.openFolder(out, ropCreateFolder, handles, hindex, hindex)
	if !ok {
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
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
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
	folder, ok := s.openFolder(out, ropDeleteFolder, handles, hindex, hindex)
	if !ok {
		return true
	}
	// Deleting a folder requires owner rights on the folder being removed.
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	if s.denyWrite(out, ropDeleteFolder, hindex, folder.store, int64(mapi.EID(fid).GCValue()), mapi.FrightsOwner) {
		return true
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
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
	newName, e5 := pullFolderString(p, uv)
	if e5 != nil {
		return false
	}

	folder, ok := s.openFolder(out, ropMoveFolder, handles, hindex, hindex)
	if !ok {
		return true
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	movedFID := int64(mapi.EID(fid).GCValue())
	// Moving (or renaming) a folder modifies the folder itself: it requires owner
	// rights on the folder being moved, the same right RopDeleteFolder requires to
	// remove one. For an owner this short-circuits.
	if s.denyWrite(out, ropMoveFolder, hindex, folder.store, movedFID, mapi.FrightsOwner) {
		return true
	}
	newParent, ok := s.moveFolderParent(out, folder, handles, hindex, dhindex)
	if !ok {
		return true
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

// emptyFolderContents soft-deletes every message in the folder and removes every
// subfolder subtree. The returned flag is the PartialCompletion the response
// carries: 1 when any single removal failed. An error means the folder could not
// be enumerated at all, which is a failed ROP rather than a partial one.
func emptyFolderContents(folder *object) (uint8, error) {
	var partial uint8
	msgs, err := folder.store.ListMessages(folder.folderID)
	if err != nil {
		return 0, err
	}
	for _, m := range msgs {
		if err := folder.store.SoftDeleteObject(m.ID); err != nil {
			partial = 1
		}
	}
	children, err := childFolders(folder.store, folder.folderID)
	if err != nil {
		return 0, err
	}
	for _, c := range children {
		if err := folder.store.DeleteFolder(c.ID); err != nil {
			partial = 1
		}
	}
	return partial, nil
}

// moveFolderParent resolves the destination handle a move reparents under. A nil
// parent means the request only renames, which is what a destination handle
// naming something other than a folder asks for.
//
// A reparent files the folder under a new parent. RenameFolder runs through the
// source store, so the new parent must be the same physical mailbox (a
// cross-mailbox reparent would collide on well-known ids); the caller then needs
// CreateSubfolder on that parent. ok is false when the response was already
// written.
func (s *Session) moveFolderParent(out *ext.Push, folder *object, handles []uint32, hindex, dhindex uint8) (*int64, bool) {
	dest := s.get(handleAt(handles, dhindex))
	if dest == nil || dest.kind != kindFolder {
		return nil, true
	}
	if dest.store == nil || folder.store.Dir() != dest.store.Dir() {
		writeErr(out, ropMoveFolder, hindex, ecNotSupported)
		return nil, false
	}
	if s.denyWrite(out, ropMoveFolder, hindex, dest.store, dest.folderID, mapi.FrightsCreateSubfolder) {
		return nil, false
	}
	return &dest.folderID, true
}

// ropCopyFolder handles RopCopyFolder ([MS-OXCFOLD] 2.2.1.4): it copies the folder
// identified by fid (with its messages, and, when WantRecursive is set, its
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
	newName, e5 := pullFolderString(p, uv)
	if e5 != nil {
		return false
	}

	folder, ok := s.openFolder(out, ropCopyFolder, handles, hindex, hindex)
	if !ok {
		return true
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	copiedFID := int64(mapi.EID(fid).GCValue())
	dest, ok := s.copyFolderDestination(out, folder, copiedFID, handles, hindex, dhindex)
	if !ok {
		return true
	}
	if _, err := folder.store.CopyFolder(copiedFID, dest.folderID, newName, wantRecursive != 0); err != nil {
		writeErr(out, ropCopyFolder, hindex, folderCopyError(err))
		return true
	}
	out.Uint8(ropCopyFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // PartialCompletion
	return true
}

// copyFolderDestination gates both sides of a folder copy and resolves the
// destination parent. ok is false when the response was already written.
//
// Copying a folder reads its contents, so it requires ReadAny on the folder
// being copied (denyWrite gates an arbitrary right, not only writes). The copy
// runs through the source store, so it lands under a parent in the same physical
// mailbox (a cross-mailbox copy would collide on well-known ids), and creating
// the new subfolder there needs CreateSubfolder. For an owner both authorize
// checks short-circuit.
func (s *Session) copyFolderDestination(out *ext.Push, folder *object, copiedFID int64, handles []uint32, hindex, dhindex uint8) (*object, bool) {
	if s.denyWrite(out, ropCopyFolder, hindex, folder.store, copiedFID, mapi.FrightsReadAny) {
		return nil, false
	}
	dest, ok := s.openFolder(out, ropCopyFolder, handles, dhindex, hindex)
	if !ok {
		return nil, false
	}
	if folder.store.Dir() != dest.store.Dir() {
		writeErr(out, ropCopyFolder, hindex, ecNotSupported)
		return nil, false
	}
	if s.denyWrite(out, ropCopyFolder, hindex, dest.store, dest.folderID, mapi.FrightsCreateSubfolder) {
		return nil, false
	}
	return dest, true
}

// folderCopyError maps a store copy failure to its ROP return code.
func folderCopyError(err error) uint32 {
	switch {
	case errors.Is(err, objectstore.ErrFolderCycle):
		return ecFolderCycle
	case errors.Is(err, objectstore.ErrNotFound):
		return ecNotFound
	}
	return ecError
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
	folder, ok := s.openFolder(out, ropHardDelMsgsAndSubfolders, handles, hindex, hindex)
	if !ok {
		return true
	}
	// Clearing a folder and dropping its subfolders deletes items: it requires
	// DeleteAny on the folder, like RopEmptyFolder.
	if s.denyWrite(out, ropHardDelMsgsAndSubfolders, hindex, folder.store, folder.folderID, mapi.FrightsDeleteAny) {
		return true
	}
	partial, err := emptyFolderContents(folder)
	if err != nil {
		writeErr(out, ropHardDelMsgsAndSubfolders, hindex, ecError)
		return true
	}
	out.Uint8(ropHardDelMsgsAndSubfolders)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(partial) // PartialCompletion
	return true
}

// ropEmptyFolder handles RopEmptyFolder ([MS-OXCFOLD] 2.2.1.5): it clears the folder's
// whole contents. Per the spec the operation spans both messages AND subfolders
// (DEL_MESSAGES | DEL_FOLDERS), so besides routing the folder's messages into the
// Recoverable Items dumpster (soft delete, recoverable until retention) it also removes
// every subfolder with its subtree. A failure to remove any single message or subfolder
// sets PartialCompletion rather than failing the whole ROP. v1 always synchronous, does
// not yet honour wantDeleteAssociated.
func (s *Session) ropEmptyFolder(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_ /* wantAsync */, e1 := p.Uint8()
	_ /* wantDeleteAssociated */, e2 := p.Uint8()
	if e1 != nil || e2 != nil {
		return false
	}
	folder, ok := s.openFolder(out, ropEmptyFolder, handles, hindex, hindex)
	if !ok {
		return true
	}
	// Emptying a folder deletes its items: it requires DeleteAny on the folder.
	if s.denyWrite(out, ropEmptyFolder, hindex, folder.store, folder.folderID, mapi.FrightsDeleteAny) {
		return true
	}
	// EmptyFolder drops the folder's messages and its subfolders, each subfolder
	// with its whole subtree.
	partial, err := emptyFolderContents(folder)
	if err != nil {
		writeErr(out, ropEmptyFolder, hindex, ecError)
		return true
	}
	out.Uint8(ropEmptyFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(partial) // PartialCompletion
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
	folder, ok := s.openFolder(out, ropHardDeleteMessages, handles, hindex, hindex)
	if !ok {
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
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
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
