package oxews

import "hermex/internal/mapi"

// PermissionSet is the EWS <t:PermissionSet> element: a folder's access-control
// list as a sequence of Permission members. The calendar variant
// (CalendarPermissionSet, whose ReadItems carries free/busy levels) is not
// modeled — v1 serves every folder as a plain <t:Folder> with PermissionSet.
type PermissionSet struct {
	Permissions []Permission `xml:"Permissions>Permission"`
}

// Permission is one <t:Permission> member. The field order matches the schema
// (BasePermissionType then the PermissionType extension): UserId, the five
// booleans, EditItems, DeleteItems, ReadItems, PermissionLevel. The booleans are
// pointers so a parsed request can tell "absent" from "false" — on read every
// flag is set, on write only the present ones are layered onto the level.
type Permission struct {
	UserID              UserID `xml:"UserId"`
	CanCreateItems      *bool  `xml:"CanCreateItems"`
	CanCreateSubFolders *bool  `xml:"CanCreateSubFolders"`
	IsFolderOwner       *bool  `xml:"IsFolderOwner"`
	IsFolderVisible     *bool  `xml:"IsFolderVisible"`
	IsFolderContact     *bool  `xml:"IsFolderContact"`
	EditItems           string `xml:"EditItems,omitempty"`
	DeleteItems         string `xml:"DeleteItems,omitempty"`
	ReadItems           string `xml:"ReadItems,omitempty"`
	PermissionLevel     string `xml:"PermissionLevel"`
}

// UserID is the <t:UserId> identity of a permission member. Only the three fields
// hermEX consumes/produces are modeled (SID and ExternalUserIdentity are ignored,
// as in the reference's own serializer). A real member is identified by
// PrimarySmtpAddress; the always-present Default and Anonymous members use
// DistinguishedUser.
type UserID struct {
	PrimarySmtpAddress string `xml:"PrimarySmtpAddress,omitempty"`
	DisplayName        string `xml:"DisplayName,omitempty"`
	DistinguishedUser  string `xml:"DistinguishedUser,omitempty"`
}

// permissionLevels maps each (non-calendar) PermissionLevelType to its rights mask.
// The masks are the project's own role composites, which equal the wire-canonical
// values at every level and, for Owner, use the full mask (mapi.RightsOwner) rather
// than the reference's frightsOwner-only quirk — matching what real clients send.
var permissionLevels = []struct {
	name string
	mask uint32
}{
	{"None", mapi.RightsNone},
	{"Owner", mapi.RightsOwner},
	{"PublishingEditor", mapi.RightsPublishingEditor},
	{"Editor", mapi.RightsEditor},
	{"PublishingAuthor", mapi.RightsPublishingAuthor},
	{"Author", mapi.RightsAuthor},
	{"NoneditingAuthor", mapi.RightsNoneditingAuthor},
	{"Reviewer", mapi.RightsReviewer},
	{"Contributor", mapi.RightsContributor},
}

// rightsForLevel returns the base rights a canned PermissionLevel seeds; Custom (or
// any unknown level) seeds 0, leaving the individual flags to carry the detail.
func rightsForLevel(level string) uint32 {
	for _, l := range permissionLevels {
		if l.name == level {
			return l.mask
		}
	}
	return 0
}

// levelForRights names the canned PermissionLevel whose mask equals the stored
// rights, or "Custom" when none does. The stored rights are normalized (the store
// fills implied bits on write — e.g. Owner implies visible|contact), so each
// profile mask is normalized before the exact compare; a raw compare would miss.
func levelForRights(rights uint32) string {
	for _, l := range permissionLevels {
		if mapi.NormalizeRights(l.mask, true) == rights {
			return l.name
		}
	}
	return "Custom"
}

// PermissionRights computes the raw rights mask a wire Permission encodes: the
// canned PermissionLevel seeds the base, then ReadItems and any present boolean
// flags are layered on (a flag sets or clears its bit; EditItems/DeleteItems'
// Owned/All add the owned/any bit). The caller is responsible for masking with
// mapi.RightsMaxROP and normalizing before the value reaches the store.
func PermissionRights(p Permission) uint32 {
	rights := rightsForLevel(p.PermissionLevel)
	if p.ReadItems == "FullDetails" {
		rights |= mapi.FrightsReadAny
	}
	setBit := func(flag *bool, bit uint32) {
		if flag == nil {
			return
		}
		if *flag {
			rights |= bit
		} else {
			rights &^= bit
		}
	}
	setBit(p.CanCreateItems, mapi.FrightsCreate)
	setBit(p.CanCreateSubFolders, mapi.FrightsCreateSubfolder)
	setBit(p.IsFolderOwner, mapi.FrightsOwner)
	setBit(p.IsFolderVisible, mapi.FrightsVisible)
	setBit(p.IsFolderContact, mapi.FrightsContact)
	switch p.EditItems {
	case "All":
		rights |= mapi.FrightsEditAny
	case "Owned":
		rights |= mapi.FrightsEditOwned
	}
	switch p.DeleteItems {
	case "All":
		rights |= mapi.FrightsDeleteAny
	case "Owned":
		rights |= mapi.FrightsDeleteOwned
	}
	return rights
}

// PermissionFromRights renders a stored permission member as a wire Permission: the
// UserId from its member id and name, the boolean flags and Edit/Delete/Read access
// from the (normalized) rights mask, and the canned PermissionLevel or Custom. A
// real member's stored name is its SMTP address; lacking a separate display name,
// it is emitted as both PrimarySmtpAddress and DisplayName.
func PermissionFromRights(memberID int64, name string, rights uint32) Permission {
	b := func(v bool) *bool { return &v }
	p := Permission{
		CanCreateItems:      b(rights&mapi.FrightsCreate != 0),
		CanCreateSubFolders: b(rights&mapi.FrightsCreateSubfolder != 0),
		IsFolderOwner:       b(rights&mapi.FrightsOwner != 0),
		IsFolderVisible:     b(rights&mapi.FrightsVisible != 0),
		IsFolderContact:     b(rights&mapi.FrightsContact != 0),
		EditItems:           accessLevel(rights, mapi.FrightsEditAny, mapi.FrightsEditOwned),
		DeleteItems:         accessLevel(rights, mapi.FrightsDeleteAny, mapi.FrightsDeleteOwned),
		ReadItems:           "None",
		PermissionLevel:     levelForRights(rights),
	}
	if rights&mapi.FrightsReadAny != 0 {
		p.ReadItems = "FullDetails"
	}
	switch memberID {
	case mapi.MemberIDDefault:
		p.UserID.DistinguishedUser = "Default"
	case mapi.MemberIDAnonymous:
		p.UserID.DistinguishedUser = "Anonymous"
	default:
		p.UserID.PrimarySmtpAddress = name
		p.UserID.DisplayName = name
	}
	return p
}

// accessLevel maps an any/owned bit pair to the EWS None/Owned/All enum.
func accessLevel(rights, anyBit, ownedBit uint32) string {
	switch {
	case rights&anyBit != 0:
		return "All"
	case rights&ownedBit != 0:
		return "Owned"
	default:
		return "None"
	}
}
