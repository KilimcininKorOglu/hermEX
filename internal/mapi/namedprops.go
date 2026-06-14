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
	// PsetidAppointment {00062002-0000-0000-C000-000000000046} holds the calendar
	// (appointment) named properties: start/end, location, busy status, subtype,
	// and sequence.
	PsetidAppointment = GUID{Data1: 0x00062002, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	// PsetidMeeting {6ED8DA90-450B-101B-98DA-00AA003F1305} holds meeting-object
	// named properties such as the global object id (UID). Unlike the other
	// namespaces it is NOT a member of the {...-C000-...-46} family — it is a
	// distinct literal GUID, so its Data2/Data3/Data4 differ.
	PsetidMeeting = GUID{Data1: 0x6ED8DA90, Data2: 0x450B, Data3: 0x101B, Data4: [8]byte{0x98, 0xDA, 0x00, 0xAA, 0x00, 0x3F, 0x13, 0x05}}
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

	// Calendar (appointment) named properties (PSETID_Appointment). DTSTART/DTEND
	// map to the "whole" start/end as a UTC PtSysTime; the time zone is carried
	// separately (deferred). NameAppointmentSubType true marks an all-day event.
	NameAppointmentSequence   = PropertyName{Kind: MnidID, GUID: PsetidAppointment, LID: 0x8201} // PtLong
	NameBusyStatus            = PropertyName{Kind: MnidID, GUID: PsetidAppointment, LID: 0x8205} // PtLong (free/tentative/busy/oof)
	NameAppointmentLocation   = PropertyName{Kind: MnidID, GUID: PsetidAppointment, LID: 0x8208} // PtUnicode
	NameAppointmentStartWhole = PropertyName{Kind: MnidID, GUID: PsetidAppointment, LID: 0x820D} // PtSysTime, UTC
	NameAppointmentEndWhole   = PropertyName{Kind: MnidID, GUID: PsetidAppointment, LID: 0x820E} // PtSysTime, UTC
	NameAppointmentSubType    = PropertyName{Kind: MnidID, GUID: PsetidAppointment, LID: 0x8215} // PtBoolean, all-day

	// Reminder named properties (PSETID_Common) — VALARM maps here.
	NameReminderDelta = PropertyName{Kind: MnidID, GUID: PsetidCommon, LID: 0x8501} // PtLong, minutes before start
	NameReminderSet   = PropertyName{Kind: MnidID, GUID: PsetidCommon, LID: 0x8503} // PtBoolean

	// Meeting named properties (PSETID_Meeting). The global object id carries the
	// iCalendar UID; v1 keeps the UID as a string property instead (the wrapped
	// binary encoding is deferred), so this is reserved for that later work.
	NameGlobalObjectId = PropertyName{Kind: MnidID, GUID: PsetidMeeting, LID: 0x0003} // PtBinary
)
