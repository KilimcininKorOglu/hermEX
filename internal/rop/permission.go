package rop

import (
	"bytes"

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

// RopModifyPermissions ModifyFlags ([MS-OXCPERM] 2.2.2): ReplaceRows clears the
// folder's whole permission set before applying the batch; IncludeFreeBusy says the
// client supplied the free/busy bits, so the server must not fill them.
const (
	modifyPermReplaceRows     uint8 = 0x01
	modifyPermIncludeFreeBusy uint8 = 0x02
)

// PermissionData row flags ([MS-OXCPERM] 2.2.2 / [MS-OXCDATA] ROWENTRY). Dispatch is
// exact equality, not a bitmask test; a flag value outside this set (e.g. the rare
// AddAndRemove) is skipped.
const (
	permRowAdd    uint8 = 0x01
	permRowModify uint8 = 0x02
	permRowRemove uint8 = 0x04
)

// abEntryIDHeaderLen is the fixed prefix of an address-book member EntryID
// ([MS-OXCDATA] 2.2.5.2 / [MS-OXNSPI] 2.2.9): flags(4) + provider GUID(16) +
// version(4) + type(4). The X500 DN follows as a NUL-terminated ASCII string.
const abEntryIDHeaderLen = 28

// ropModifyPermissions handles RopModifyPermissions ([MS-OXCPERM] 2.2.2): it decodes
// the PermissionData rows, turns each into a store change, and applies the batch. The
// response is the bare head whose HandleIndex echoes the input folder handle.
//
// Access is store-owner authorized (see ropGetPermissionsTable): the caller is always
// the mailbox owner today, so the frightsOwner gate passes by the owner bypass.
func (s *Session) ropModifyPermissions(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	flags, e1 := p.Uint8()  // ModifyFlags
	count, e2 := p.Uint16() // PermissionsCount
	if e1 != nil || e2 != nil {
		return false
	}
	type permRow struct {
		flags    uint8
		propvals mapi.PropertyValues
	}
	rows := make([]permRow, 0, count)
	for i := 0; i < int(count); i++ {
		rowFlags, e3 := p.Uint8()
		propvals, e4 := p.PropertyValues()
		if e3 != nil || e4 != nil {
			return false
		}
		rows = append(rows, permRow{flags: rowFlags, propvals: propvals})
	}

	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropModifyPermissions, hindex, ecError)
		return true
	}
	// Editing a folder's permission table requires owner rights — the gate that
	// stops a delegate from granting themselves broader access on the owner's folders.
	if s.denyWrite(out, ropModifyPermissions, hindex, folder.store, folder.folderID, mapi.FrightsOwner) {
		return true
	}

	includeFB := flags&modifyPermIncludeFreeBusy != 0
	changes := make([]objectstore.PermissionChange, 0, len(rows))
	for _, r := range rows {
		switch r.flags {
		case permRowAdd:
			username, ok := s.resolvePermissionMember(r.propvals)
			if !ok {
				continue // a member that cannot be resolved is skipped, never faulted
			}
			changes = append(changes, objectstore.PermissionChange{
				Op: objectstore.PermAdd, Username: username, Rights: ingestRights(r.propvals, includeFB),
			})
		case permRowModify:
			id, ok := memberID(r.propvals)
			if !ok {
				continue
			}
			changes = append(changes, objectstore.PermissionChange{
				Op: objectstore.PermModify, MemberID: id, Rights: ingestRights(r.propvals, includeFB),
			})
		case permRowRemove:
			id, ok := memberID(r.propvals)
			if !ok {
				continue
			}
			changes = append(changes, objectstore.PermissionChange{Op: objectstore.PermRemove, MemberID: id})
		}
	}

	if err := folder.store.ModifyPermissions(folder.folderID, flags&modifyPermReplaceRows != 0, changes); err != nil {
		writeErr(out, ropModifyPermissions, hindex, ecError)
		return true
	}

	out.Uint8(ropModifyPermissions)
	out.Uint8(hindex) // echo the input handle
	out.Uint32(ecSuccess)
	return true
}

// memberID reads PR_MEMBER_ID (PtI8) from a permission row's property bag.
func memberID(propvals mapi.PropertyValues) (int64, bool) {
	v, ok := propvals.Get(mapi.PrMemberID)
	if !ok {
		return 0, false
	}
	id, ok := v.(int64)
	return id, ok
}

// ingestRights reads PR_MEMBER_RIGHTS, masks it to the client-sendable set, then
// applies the implied-rights normalization a server owes on ingest. A missing
// PR_MEMBER_RIGHTS is rightsNone. The mask is security-relevant: it drops bits a
// client must not set (store-owner, send-as) before they reach the store.
func ingestRights(propvals mapi.PropertyValues, includeFreeBusy bool) uint32 {
	var rights uint32
	if v, ok := propvals.Get(mapi.PrMemberRights); ok {
		if r, ok := v.(int32); ok {
			rights = uint32(r)
		}
	}
	return mapi.NormalizeRights(rights&mapi.RightsMaxROP, !includeFreeBusy)
}

// resolvePermissionMember resolves the storage username for a real-member AddRow from
// its address identity: the member's PR_ENTRYID (an address-book EntryID whose X500
// DN embeds the SMTP address) first, then a literal PR_SMTP_ADDRESS — the reference's
// precedence. When the session has a directory the address is confirmed to exist; an
// unresolvable row yields ("", false) so the caller skips it rather than faulting.
func (s *Session) resolvePermissionMember(propvals mapi.PropertyValues) (string, bool) {
	var smtp string
	if v, ok := propvals.Get(mapi.PrEntryID); ok {
		if b, ok := v.([]byte); ok {
			smtp = smtpFromABEntryID(b)
		}
	}
	if smtp == "" {
		if v, ok := propvals.Get(mapi.PrSmtpAddress); ok {
			if addr, ok := v.(string); ok {
				smtp = addr
			}
		}
	}
	if smtp == "" {
		return "", false
	}
	if s.accounts != nil {
		if _, ok := s.accounts.Resolve(smtp); !ok {
			return "", false // unknown member; skip
		}
	}
	return smtp, true
}

// smtpFromABEntryID extracts the SMTP address a member EntryID carries: it reads the
// X500 DN after the fixed AB-EntryID header and returns the segment after its last
// "/cn=" (hermEX's GAL DN embeds the SMTP there, and an SMTP address never contains a
// slash, so LastIndex is exact). It returns "" for anything that is not a recognizable
// AB EntryID DN, so the caller falls back to PR_SMTP_ADDRESS.
func smtpFromABEntryID(eid []byte) string {
	if len(eid) <= abEntryIDHeaderLen {
		return ""
	}
	dn := eid[abEntryIDHeaderLen:]
	if i := bytes.IndexByte(dn, 0); i >= 0 {
		dn = dn[:i]
	}
	const cn = "/cn="
	i := bytes.LastIndex(dn, []byte(cn))
	if i < 0 {
		return ""
	}
	return string(dn[i+len(cn):])
}
