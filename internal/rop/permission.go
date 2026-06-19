package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// permTableIncludeFreeBusy is the only standard RopGetPermissionsTable TableFlags bit
// ([MS-OXCPERM] 2.2.1.1): when set the served PR_MEMBER_RIGHTS keeps the free/busy
// bits, else they are masked out. (The reference-internal RopFilter 0x100 does not
// fit this single request octet and never arrives from a real client.)
const permTableIncludeFreeBusy uint8 = 0x02

// ropGetPermissionsTable handles RopGetPermissionsTable ([MS-OXCPERM] 2.2.1): it
// snapshots the folder's permission members into a new table object whose rows the
// client reads with RopSetColumns/RopQueryRows. The response is the bare 6-byte head
// — no row count, matching the reference's no-extra-body encoding — so the output
// handle (HandleIndex) is the only thing the client needs to start paging.
//
// Access is store-owner authorized: a hermEX session only ever opens its own mailbox,
// so the caller is always the folder's owner and the [MS-OXCPERM] frightsOwner gate
// passes by the owner bypass. When delegate/shared-mailbox opens arrive, a non-owner
// caller must be checked against frightsOwner|frightsVisible here.
func (s *Session) ropGetPermissionsTable(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8() // OutputHandleIndex
	flags, e2 := p.Uint8()   // TableFlags
	if e1 != nil || e2 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropGetPermissionsTable, ohindex, ecError)
		return true
	}
	bags, err := permissionBags(folder.store, folder.folderID, flags&permTableIncludeFreeBusy != 0)
	if err != nil {
		writeErr(out, ropGetPermissionsTable, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{
		kind:  kindTable,
		store: folder.store,
		table: &tableState{kind: tablePermission, permissions: bags},
	})
	setHandle(handles, ohindex, h)

	out.Uint8(ropGetPermissionsTable)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}

// permissionBags builds one property bag per permission member the table serves:
// PR_MEMBER_ID, PR_MEMBER_NAME, PR_MEMBER_RIGHTS, and a present-empty PR_ENTRYID
// (matching the reference's empty member EntryID; a real member's address-book
// EntryID is a v1 gap — it is informational, since Modify/Remove key on PR_MEMBER_ID).
// The always-present Default (id 0) and Anonymous (id -1) members are synthesized at
// rightsNone when the folder stores no row for them, checked against the wire member
// ids ListPermissions already translated so a stored default/anonymous is not
// duplicated. Without the IncludeFreeBusy flag the free/busy rights are masked off.
func permissionBags(store *objectstore.Store, folderID int64, includeFreeBusy bool) ([]mapi.PropertyValues, error) {
	entries, err := store.ListPermissions(folderID)
	if err != nil {
		return nil, err
	}

	haveDefault, haveAnon := false, false
	for _, e := range entries {
		switch e.MemberID {
		case mapi.MemberIDDefault:
			haveDefault = true
		case mapi.MemberIDAnonymous:
			haveAnon = true
		}
	}
	if !haveDefault {
		entries = append(entries, objectstore.PermissionEntry{MemberID: mapi.MemberIDDefault, Name: "default", Rights: mapi.RightsNone})
	}
	if !haveAnon {
		entries = append(entries, objectstore.PermissionEntry{MemberID: mapi.MemberIDAnonymous, Name: "anonymous", Rights: mapi.RightsNone})
	}

	bags := make([]mapi.PropertyValues, 0, len(entries))
	for _, e := range entries {
		rights := e.Rights
		if !includeFreeBusy {
			rights &^= mapi.FrightsFreeBusySimple | mapi.FrightsFreeBusyDetailed
		}
		var bag mapi.PropertyValues
		bag.Set(mapi.PrMemberID, e.MemberID)        // PtI8
		bag.Set(mapi.PrMemberName, e.Name)          // PtUnicode
		bag.Set(mapi.PrMemberRights, int32(rights)) // PtLong
		bag.Set(mapi.PrEntryID, []byte{})           // present-empty member EntryID
		bags = append(bags, bag)
	}
	return bags, nil
}
