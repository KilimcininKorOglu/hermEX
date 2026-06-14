package mapi

// Standard property tags (MS-OXPROPS). Constants are added per consumer; this
// set covers the folder property bag the object store writes when seeding and
// creating folders.
const (
	PrDisplayName               = PropTag(0x3001001F) // PtUnicode
	PrComment                   = PropTag(0x3004001F) // PtUnicode
	PrContainerClass            = PropTag(0x3613001F) // PtUnicode
	PrCreationTime              = PropTag(0x30070040) // PtSysTime
	PrLastModificationTime      = PropTag(0x30080040) // PtSysTime
	PrHierRev                   = PropTag(0x40820040) // PtSysTime
	PrLocalCommitTimeMax        = PropTag(0x670A0040) // PtSysTime
	PrChangeKey                 = PropTag(0x65E20102) // PtBinary (XID)
	PrPredecessorChangeList     = PropTag(0x65E30102) // PtBinary (PCL)
	PrAttrHidden                = PropTag(0x10F4000B) // PtBoolean
	PrInternetArticleNumber     = PropTag(0x0E230003) // PtLong
	PrInternetArticleNumberNext = PropTag(0x67510003) // PtLong
	PrDeletedCountTotal         = PropTag(0x670B0003) // PtLong
	PrDeletedFolderCount        = PropTag(0x66410003) // PtLong
	PrHierarchyChangeNum        = PropTag(0x663E0003) // PtLong
	PrParentFolderID            = PropTag(0x67490014) // PtI8 (PidTagParentFolderId)
	PrFolderID                  = PropTag(0x67480014) // PtI8 (PidTagFolderId)
)

// Store-root property tags written when seeding a mailbox.
const (
	PrOOFState                  = PropTag(0x661D000B) // PtBoolean (out-of-office)
	PrMessageSizeExtended       = PropTag(0x0E080014) // PtI8
	PrNormalMessageSizeExtended = PropTag(0x66B30014) // PtI8
	PrAssocMessageSizeExtended  = PropTag(0x66B40014) // PtI8
	// PrWebmailSettings is the provider-defined store-root property (0x6772)
	// that holds the webmail client's settings tree as a JSON string, so
	// settings and signatures persist as a MAPI property rather than in a
	// dedicated table.
	PrWebmailSettings = PropTag(0x6772001F) // PtUnicode
	// PrOOFSettings is the provider-defined store-root property (0x6773) holding
	// the out-of-office configuration (reply text, subject, schedule) as a JSON
	// string. The on/off flag is mirrored into the standard PrOOFState boolean.
	PrOOFSettings = PropTag(0x6773001F) // PtUnicode
	// PrSmimeIdentity is the provider-defined store-root property (0x6775) holding
	// the user's S/MIME identity as JSON: the password-protected PKCS#12 container
	// and its public certificate, both base64. The container stays encrypted at
	// rest under its own passphrase, which the webmail unlocks per session and
	// never persists.
	PrSmimeIdentity = PropTag(0x6775001F) // PtUnicode
	// PrSmimeCertStore is the provider-defined store-root property (0x6776) holding
	// recipient certificates for encryption as a JSON map of address to base64 DER,
	// uploaded by the user or harvested from verified signed mail.
	PrSmimeCertStore = PropTag(0x6776001F) // PtUnicode
)

// Large message/attachment content property tags. These hold bodies and
// attachment payloads and are offloaded to content-addressed files rather than
// stored inline in the property tables.
const (
	PrBody                     = PropTag(0x1000001F) // PtUnicode (PidTagBody)
	PrBodyA                    = PropTag(0x1000001E) // PtString8
	PrHTML                     = PropTag(0x10130102) // PtBinary (PidTagHtml)
	PrRTFCompressed            = PropTag(0x10090102) // PtBinary
	PrTransportMessageHeaders  = PropTag(0x007D001F) // PtUnicode
	PrTransportMessageHeadersA = PropTag(0x007D001E) // PtString8
	PrAttachDataBin            = PropTag(0x37010102) // PtBinary (PidTagAttachDataBinary)
	PrAttachDataObj            = PropTag(0x3701000D) // PtObject
	// PrSmimeOriginal is the provider-defined message property (0x6774) holding
	// the original wire bytes of a signed or encrypted S/MIME message. The store
	// serves these verbatim instead of re-synthesizing the message, because
	// oxcmail.Export rebuilds the MIME tree and would invalidate the signature or
	// mangle the envelope. Offloaded to a content file like other large content.
	PrSmimeOriginal = PropTag(0x67740102) // PtBinary
)

// Message envelope property tags (MS-OXCMAIL / MS-OXOMSG): the standard
// header-derived properties an imported message carries.
const (
	PrMessageClass        = PropTag(0x001A001F) // PtUnicode (PidTagMessageClass)
	PrSubject             = PropTag(0x0037001F) // PtUnicode
	PrSubjectPrefix       = PropTag(0x003D001F) // PtUnicode
	PrNormalizedSubject   = PropTag(0x0E1D001F) // PtUnicode
	PrImportance          = PropTag(0x00170003) // PtLong
	PrSensitivity         = PropTag(0x00360003) // PtLong
	PrClientSubmitTime    = PropTag(0x00390040) // PtSysTime
	PrMessageDeliveryTime = PropTag(0x0E060040) // PtSysTime
	PrDeferredSendTime    = PropTag(0x3FEF0040) // PtSysTime (PidTagDeferredSendTime, MS-OXOMSG) — absolute time to release a deferred send
	PrMessageFlags        = PropTag(0x0E070003) // PtLong
	PrMessageSize         = PropTag(0x0E080003) // PtLong (PidTagMessageSize) — total message size in bytes
	PrFlagStatus          = PropTag(0x10900003) // PtLong (PidTagFlagStatus, MS-OXOFLAG) — 0 none / 1 complete / 2 flagged
	PrFollowupIcon        = PropTag(0x10950003) // PtLong (PidTagFollowupIcon) — flag color: 0 clear / 1 purple / 2 orange / 3 green / 4 yellow / 5 blue / 6 red
	PrFlagCompleteTime    = PropTag(0x10910040) // PtSysTime (PidTagFlagCompleteTime) — when the flag was marked complete
	PrInternetMessageID   = PropTag(0x1035001F) // PtUnicode
	PrInReplyToID         = PropTag(0x1042001F) // PtUnicode
	PrInternetReferences  = PropTag(0x1039001F) // PtUnicode
	PrConversationTopic   = PropTag(0x0070001F) // PtUnicode
	PrConversationIndex   = PropTag(0x00710102) // PtBinary
	PrInternetCodepage    = PropTag(0x3FDE0003) // PtLong (PidTagInternetCodepage)
	PrPriority            = PropTag(0x00260003) // PtLong
)

// Message importance/sensitivity/flags values (MS-OXCMSG).
const (
	ImportanceLow    = 0
	ImportanceNormal = 1
	ImportanceHigh   = 2

	SensitivityNone         = 0
	SensitivityPersonal     = 1
	SensitivityPrivate      = 2
	SensitivityConfidential = 3

	MsgFlagRead   = 0x00000001 // mfRead
	MsgFlagUnsent = 0x00000008 // mfUnsent
)

// Sender and sent-representing identity property tags. Import fills both sets,
// falling back one to the other when a message names only one identity.
const (
	PrSenderName         = PropTag(0x0C1A001F) // PtUnicode
	PrSenderAddrType     = PropTag(0x0C1E001F) // PtUnicode
	PrSenderEmailAddress = PropTag(0x0C1F001F) // PtUnicode
	PrSenderSmtpAddress  = PropTag(0x5D01001F) // PtUnicode
	PrSenderEntryID      = PropTag(0x0C190102) // PtBinary
	PrSenderSearchKey    = PropTag(0x0C1D0102) // PtBinary

	PrSentRepresentingName         = PropTag(0x0042001F) // PtUnicode
	PrSentRepresentingAddrType     = PropTag(0x0064001F) // PtUnicode
	PrSentRepresentingEmailAddress = PropTag(0x0065001F) // PtUnicode
	PrSentRepresentingSmtpAddress  = PropTag(0x5D02001F) // PtUnicode
	PrSentRepresentingEntryID      = PropTag(0x00410102) // PtBinary
	PrSentRepresentingSearchKey    = PropTag(0x003B0102) // PtBinary
)

// Read-receipt (MDN) property tags. A Disposition-Notification-To header sets
// PR_READ_RECEIPT_REQUESTED plus the notification address, which is re-emitted on
// export. The entryid is not synthesized (the mail path reads only name and
// address), matching the deferral applied to sender and recipient identities.
const (
	PrReadReceiptRequested            = PropTag(0x0029000B) // PtBoolean
	PrNonReceiptNotificationRequested = PropTag(0x0C06000B) // PtBoolean
	PrReadReceiptName                 = PropTag(0x402B001F) // PtUnicode
	PrReadReceiptAddrType             = PropTag(0x4029001F) // PtUnicode
	PrReadReceiptEmailAddress         = PropTag(0x402A001F) // PtUnicode
	PrReadReceiptSmtpAddress          = PropTag(0x5D05001F) // PtUnicode
	PrReadReceiptSearchKey            = PropTag(0x00530102) // PtBinary
	PrReadReceiptEntryID              = PropTag(0x00460102) // PtBinary
)

// Recipient property tags (one bag per recipient in the recipient table).
const (
	PrRecipientType           = PropTag(0x0C150003) // PtLong (mapiTo/Cc/Bcc)
	PrAddrType                = PropTag(0x3002001F) // PtUnicode
	PrEmailAddress            = PropTag(0x3003001F) // PtUnicode
	PrSmtpAddress             = PropTag(0x39FE001F) // PtUnicode
	PrTransmitableDisplayName = PropTag(0x3A20001F) // PtUnicode
	PrSearchKey               = PropTag(0x300B0102) // PtBinary
	PrEntryID                 = PropTag(0x0FFF0102) // PtBinary
	PrRecipientEntryID        = PropTag(0x5FF70102) // PtBinary
	PrRecordKey               = PropTag(0x0FF90102) // PtBinary
	PrObjectType              = PropTag(0x0FFE0003) // PtLong
	PrDisplayType             = PropTag(0x39000003) // PtLong
	PrResponsibility          = PropTag(0x0E0F000B) // PtBoolean
	PrRecipientFlags          = PropTag(0x5FFD0003) // PtLong
)

// Recipient type values (PR_RECIPIENT_TYPE).
const (
	RecipTo  = 1 // mapiTo
	RecipCc  = 2 // mapiCc
	RecipBcc = 3 // mapiBcc
)

// Object/display type and recipient-flag values (MS-OXCDATA / MS-OXOABK).
const (
	ObjectTypeMailUser = 6 // mapi_object_type::mailuser
	ObjectTypeDistList = 8 // mapi_object_type::distlist

	DisplayTypeMailUser = 0 // DT_MAILUSER

	RecipientSendable = 0x1 // recipSendable
)

// Attachment property tags (one bag per attachment in the attachment table).
// PR_ATTACH_DATA_BIN lives in the content-offload block above.
const (
	PrAttachLongFilename = PropTag(0x3707001F) // PtUnicode
	PrAttachFilename     = PropTag(0x3704001F) // PtUnicode (8.3 form)
	PrAttachExtension    = PropTag(0x3703001F) // PtUnicode
	PrAttachMimeTag      = PropTag(0x370E001F) // PtUnicode
	PrAttachContentID    = PropTag(0x3712001F) // PtUnicode
	PrAttachMethod       = PropTag(0x37050003) // PtLong
	PrAttachFlags        = PropTag(0x37140003) // PtLong
	PrRenderingPosition  = PropTag(0x370B0003) // PtLong
	PrAttachNum          = PropTag(0x0E210003) // PtLong (store-assigned)
)

// Attachment method (PR_ATTACH_METHOD) and flag (PR_ATTACH_FLAGS) values.
const (
	AttachNone        = 0    // afNone
	AttachByValue     = 1    // afByValue
	AttachEmbeddedMsg = 5    // afEmbeddedMessage
	AttMhtmlRef       = 0x04 // attachment referenced by the HTML body (inline)
)

// Common container classes for default folders (PR_CONTAINER_CLASS values).
const (
	ContainerClassNote        = "IPF.Note"        // mail folders
	ContainerClassAppointment = "IPF.Appointment" // calendar
	ContainerClassContact     = "IPF.Contact"     // contacts
	ContainerClassTask        = "IPF.Task"        // tasks
	ContainerClassStickyNote  = "IPF.StickyNote"  // notes
	ContainerClassJournal     = "IPF.Journal"     // journal
)

// Contact (IPM.Contact) properties (PidTag*, MS-OXOCNTC). The email addresses,
// work address, file-as, IM address, and has-picture flag are NAMED properties
// in PSETID_Address (see namedprops.go); the tags below are the static PidTag
// contact properties carried directly on the message object.
const (
	// Name and identity.
	PrGivenName         = PropTag(0x3A06001F) // PtUnicode (PidTagGivenName)
	PrSurname           = PropTag(0x3A11001F) // PtUnicode (PidTagSurname)
	PrMiddleName        = PropTag(0x3A44001F) // PtUnicode (PidTagMiddleName)
	PrDisplayNamePrefix = PropTag(0x3A45001F) // PtUnicode (PidTagDisplayNamePrefix, e.g. "Dr.")
	PrGeneration        = PropTag(0x3A05001F) // PtUnicode (PidTagGeneration, e.g. "Jr.")
	PrNickname          = PropTag(0x3A4F001F) // PtUnicode (PidTagNickname)
	PrTitle             = PropTag(0x3A17001F) // PtUnicode (PidTagTitle, job title)
	PrCompanyName       = PropTag(0x3A16001F) // PtUnicode (PidTagCompanyName)
	PrDepartmentName    = PropTag(0x3A18001F) // PtUnicode (PidTagDepartmentName)
	PrProfession        = PropTag(0x3A46001F) // PtUnicode (PidTagProfession)
	PrBirthday          = PropTag(0x3A420040) // PtSysTime (PidTagBirthday)
	PrBusinessHomePage  = PropTag(0x3A51001F) // PtUnicode (PidTagBusinessHomePage)
	PrPersonalHomePage  = PropTag(0x3A50001F) // PtUnicode (PidTagPersonalHomePage)

	// Telephone numbers.
	PrBusinessTelephoneNumber  = PropTag(0x3A08001F) // PtUnicode (also the office number)
	PrHomeTelephoneNumber      = PropTag(0x3A09001F) // PtUnicode
	PrPrimaryTelephoneNumber   = PropTag(0x3A1A001F) // PtUnicode
	PrBusiness2TelephoneNumber = PropTag(0x3A1B001F) // PtUnicode
	PrMobileTelephoneNumber    = PropTag(0x3A1C001F) // PtUnicode
	PrCarTelephoneNumber       = PropTag(0x3A1E001F) // PtUnicode
	PrOtherTelephoneNumber     = PropTag(0x3A1F001F) // PtUnicode
	PrPagerTelephoneNumber     = PropTag(0x3A21001F) // PtUnicode
	PrBusinessFaxNumber        = PropTag(0x3A24001F) // PtUnicode
	PrHomeFaxNumber            = PropTag(0x3A25001F) // PtUnicode
	PrHome2TelephoneNumber     = PropTag(0x3A2F001F) // PtUnicode

	// Home postal address.
	PrHomeAddressStreet          = PropTag(0x3A5D001F) // PtUnicode
	PrHomeAddressCity            = PropTag(0x3A59001F) // PtUnicode
	PrHomeAddressStateOrProvince = PropTag(0x3A5C001F) // PtUnicode
	PrHomeAddressPostalCode      = PropTag(0x3A5B001F) // PtUnicode
	PrHomeAddressCountry         = PropTag(0x3A5A001F) // PtUnicode
	PrHomeAddressPostOfficeBox   = PropTag(0x3A5E001F) // PtUnicode

	// Other postal address.
	PrOtherAddressStreet          = PropTag(0x3A63001F) // PtUnicode
	PrOtherAddressCity            = PropTag(0x3A5F001F) // PtUnicode
	PrOtherAddressStateOrProvince = PropTag(0x3A62001F) // PtUnicode
	PrOtherAddressPostalCode      = PropTag(0x3A61001F) // PtUnicode
	PrOtherAddressCountry         = PropTag(0x3A60001F) // PtUnicode
	PrOtherAddressPostOfficeBox   = PropTag(0x3A64001F) // PtUnicode
)
