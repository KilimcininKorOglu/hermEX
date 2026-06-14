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
	// PsetidAddress {00062004-0000-0000-C000-000000000046} holds the contact
	// (person) named properties: the three email slots, the work address,
	// file-as, instant-messaging address, and the has-picture flag.
	PsetidAddress = GUID{Data1: 0x00062004, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
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

	// Contact email slots (PidLidEmail{1,2,3}*, PSETID_Address, PtUnicode). Each
	// slot carries an SMTP address, a display name, and an address type.
	NameEmail1Address     = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8083}
	NameEmail1DisplayName = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8080}
	NameEmail1AddressType = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8082}
	NameEmail2Address     = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8093}
	NameEmail2DisplayName = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8090}
	NameEmail2AddressType = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8092}
	NameEmail3Address     = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x80A3}
	NameEmail3DisplayName = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x80A0}
	NameEmail3AddressType = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x80A2}

	// Contact work (business) postal address (PidLidWorkAddress*, PSETID_Address,
	// PtUnicode). The home and other addresses are static PidTag properties.
	NameWorkAddressStreet        = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8045}
	NameWorkAddressCity          = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8046}
	NameWorkAddressState         = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8047}
	NameWorkAddressPostalCode    = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8048}
	NameWorkAddressCountry       = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8049}
	NameWorkAddressPostOfficeBox = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x804A}

	// Other contact named properties (PSETID_Address).
	NameFileAs                  = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8005} // PtUnicode, file-as / sort name
	NameInstantMessagingAddress = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8062} // PtUnicode
	NameHasPicture              = PropertyName{Kind: MnidID, GUID: PsetidAddress, LID: 0x8015} // PtBoolean, contact photo present
)
