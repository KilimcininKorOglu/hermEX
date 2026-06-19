package mapi

// Folder-permission rights model (MS-OXCPERM). PR_MEMBER_RIGHTS is a bitfield of
// the frights* bits below; the canonical Outlook permission levels (Reviewer,
// Author, …) are fixed unions of those bits. The store keeps the post-mask,
// post-normalization value; the wire carries the same uint32.

// frights* are the PidTagMemberRights bits ([MS-OXCPERM] 2.2.7). They are kept as
// the spec's frights names (exported) so a reader can grep the wire constant.
const (
	FrightsReadAny          uint32 = 0x0001 // read any item in the folder
	FrightsCreate           uint32 = 0x0002 // create new items
	FrightsEditOwned        uint32 = 0x0008 // edit items the member created
	FrightsDeleteOwned      uint32 = 0x0010 // delete items the member created
	FrightsEditAny          uint32 = 0x0020 // edit any item
	FrightsDeleteAny        uint32 = 0x0040 // delete any item
	FrightsCreateSubfolder  uint32 = 0x0080 // create subfolders
	FrightsOwner            uint32 = 0x0100 // folder owner (full control)
	FrightsContact          uint32 = 0x0200 // folder contact
	FrightsVisible          uint32 = 0x0400 // folder is visible to the member
	FrightsFreeBusySimple   uint32 = 0x0800 // read free/busy time only
	FrightsFreeBusyDetailed uint32 = 0x1000 // read free/busy detail (subject/location)
)

// RightsAll is full ownership: every standard right except the free/busy-only and
// contact bits the dedicated roles add. RightsMaxROP is the set a client may set
// over the wire — ModifyPermissions ingest masks each row with it, so any bit
// outside this allowlist (reference-private extensions, reserved bits) is dropped
// before storage. Both are symbolic unions so they cannot transcribe-error.
const (
	RightsNone   uint32 = 0
	RightsAll    uint32 = FrightsReadAny | FrightsCreate | FrightsEditOwned | FrightsDeleteOwned | FrightsEditAny | FrightsDeleteAny | FrightsCreateSubfolder | FrightsOwner | FrightsVisible
	RightsMaxROP uint32 = RightsAll | FrightsContact | FrightsFreeBusySimple | FrightsFreeBusyDetailed
)

// The canonical Outlook permission levels ([MS-OXCPERM] role table). Each is a
// fixed union of frights bits; a client that picks a role sends the matching mask.
const (
	RightsReviewer         uint32 = FrightsReadAny | FrightsVisible
	RightsContributor      uint32 = FrightsVisible | FrightsCreate
	RightsNoneditingAuthor uint32 = FrightsReadAny | FrightsVisible | FrightsCreate | FrightsDeleteOwned
	RightsAuthor           uint32 = RightsNoneditingAuthor | FrightsEditOwned
	RightsPublishingAuthor uint32 = RightsAuthor | FrightsCreateSubfolder
	RightsEditor           uint32 = FrightsReadAny | FrightsVisible | FrightsCreate | FrightsDeleteOwned | FrightsEditOwned | FrightsEditAny | FrightsDeleteAny
	RightsPublishingEditor uint32 = RightsEditor | FrightsCreateSubfolder
	RightsOwner            uint32 = RightsAll
)

// MemberIDDefault and MemberIDAnonymous are the special PR_MEMBER_ID values
// ([MS-OXCPERM] 2.2.4). They are NOT row ids: the store keeps the default member
// under username "default" and the anonymous member under "" (empty), each with
// its own row id, while the wire always carries 0 / -1 for them. PR_MEMBER_ID is
// PtI8 (signed 64-bit), so -1 serializes as 0xFFFFFFFFFFFFFFFF.
const (
	MemberIDDefault   int64 = 0
	MemberIDAnonymous int64 = -1
)

// NormalizeRights applies the MS-OXCPERM implied-rights normalization a server must
// perform on ModifyPermissions ingest: a coarse right implies the narrower one it
// supersedes, and (unless the client claimed free/busy control) the server fills in
// the free/busy bits. Callers mask with RightsMaxROP first, then normalize.
//
// adjustFreeBusy is the negation of the request's INCLUDEFREEBUSY flag: when the
// client did not supply free/busy bits, the server grants free/busy access implied
// by the member's read rights.
func NormalizeRights(rights uint32, adjustFreeBusy bool) uint32 {
	if rights&FrightsReadAny != 0 {
		rights |= FrightsVisible
	}
	if rights&FrightsEditAny != 0 {
		rights |= FrightsEditOwned
	}
	if rights&FrightsDeleteAny != 0 {
		rights |= FrightsDeleteOwned
	}
	if rights&FrightsOwner != 0 {
		rights |= FrightsVisible | FrightsContact
	}
	if adjustFreeBusy {
		rights |= FrightsFreeBusySimple
		if rights&FrightsReadAny != 0 {
			rights |= FrightsFreeBusyDetailed
		}
	}
	return rights
}
