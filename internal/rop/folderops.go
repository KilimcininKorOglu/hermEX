package rop

import (
	"hermex/internal/ext"
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
	folderID, err := folder.store.CreateFolder(&folder.folderID, name)
	if err != nil {
		writeErr(out, ropCreateFolder, hindex, ecError)
		return true
	}
	out.Uint8(ropCreateFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint64(uint64(folderID)) // FolderId (FID)
	out.Uint8(0)                 // IsExisting
	out.Uint8(0)                 // HasRules
	out.Uint64(0)                // Ghost (unused)
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
	if err := folder.store.DeleteFolder(int64(fid)); err != nil {
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
	dest := s.get(handleAt(handles, dhindex))
	var newParent *int64
	if dest != nil && dest.kind == kindFolder {
		newParent = &dest.folderID
	}
	if err := folder.store.RenameFolder(int64(fid), newParent, newName); err != nil {
		writeErr(out, ropMoveFolder, hindex, ecError)
		return true
	}
	out.Uint8(ropMoveFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // PartialCompletion
	return true
}

// ropCopyFolder handles RopCopyFolder ([MS-OXCFOLD] 2.2.1.4): it copies a folder
// and its contents to a destination folder. v1 returns ecNotSupported because
// recursive copy semantics are complex and no consumer is known to require this yet.
// The body is consumed so the parser stays aligned in a multi-ROP batch.
func (s *Session) ropCopyFolder(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_ /* dhindex */, e0 := p.Uint8()
	_ /* wantAsync */, e1 := p.Uint8()
	_ /* wantRecursive */, e2 := p.Uint8()
	uv, e3 := p.Uint8()
	_ /* fid */, e4 := p.Uint64()
	if e0 != nil || e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	if uv != 0 {
		_, e5 := p.Unicode()
		if e5 != nil {
			return false
		}
	} else {
		_, e5 := p.String8()
		if e5 != nil {
			return false
		}
	}
	writeErr(out, ropCopyFolder, hindex, ecNotSupported)
	return true
}

// ropEmptyFolder handles RopEmptyFolder ([MS-OXCFOLD] 2.2.1.5): it deletes all
// messages (and optionally FAI/associated items) from the folder. v1 always
// synchronous, does not yet honour wantDeleteAssociated.
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
	msgs, err := folder.store.ListMessages(folder.folderID)
	if err != nil {
		writeErr(out, ropEmptyFolder, hindex, ecError)
		return true
	}
	for _, m := range msgs {
		if err := folder.store.DeleteObject(m.ID); err != nil {
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

// ropHardDeleteMessages handles RopHardDeleteMessages ([MS-OXCSTOR] 2.2.1.1): it
// permanently deletes messages from the store without moving them to DeletedItems.
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
	// MessageIds is a flat sequence of 8-byte little-endian IDs
	for i := 0; i+8 <= len(mids); i += 8 {
		mid := int64(uint64(mids[i]) | uint64(mids[i+1])<<8 |
			uint64(mids[i+2])<<16 | uint64(mids[i+3])<<24 |
			uint64(mids[i+4])<<32 | uint64(mids[i+5])<<40 |
			uint64(mids[i+6])<<48 | uint64(mids[i+7])<<56)
		// The spec says these are FIDs (folder IDs), but the reference passes
		// them to delete_folder_messages which expects message IDs. In v1 we
		// interpret them as message object IDs — the store's DeleteObject.
		_ = mid
		if err := folder.store.DeleteObject(mid); err != nil {
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
func (s *Session) ropSetSearchCriteria(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
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
func (s *Session) ropGetSearchCriteria(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
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
