package mapi

// Well-known named-property namespaces (MS-OXPROPS §1.3.2) and the named
// properties the mail features resolve through them. A named property is
// identified by its GUID namespace plus either a long id (MnidID) or a name
// string (MnidString); the store's allocator maps each to a stable property id.

var (
	// PsetidCommon {00062008-0000-0000-C000-000000000046} holds the common
	// follow-up and reminder named properties.
	PsetidCommon = GUID{Data1: 0x00062008, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	// PsPublicStrings {00020329-0000-0000-C000-000000000046} is the public string
	// namespace; the "Keywords" name under it holds a message's categories.
	PsPublicStrings = GUID{Data1: 0x00020329, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
)

var (
	// NameFlagRequest (PidLidFlagRequest, PSETID_Common/0x8530, PtUnicode) is the
	// flag's follow-up text, e.g. "Follow up".
	NameFlagRequest = PropertyName{Kind: MnidID, GUID: PsetidCommon, LID: 0x8530}
	// NameReminderSignalTime (PidLidReminderSignalTime, PSETID_Common/0x8560,
	// PtSysTime) carries a flag's due / reminder time.
	NameReminderSignalTime = PropertyName{Kind: MnidID, GUID: PsetidCommon, LID: 0x8560}
	// NameKeywords (PidNameKeywords, PS_PUBLIC_STRINGS/"Keywords", PtMvUnicode) is
	// a message's category list.
	NameKeywords = PropertyName{Kind: MnidString, GUID: PsPublicStrings, Name: "Keywords"}
)
