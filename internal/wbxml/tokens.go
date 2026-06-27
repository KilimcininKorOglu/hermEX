package wbxml

// Tag identifies an ActiveSync element: the high byte is the WBXML code page
// and the low byte is the tag token within that page (MS-ASWBXML). The token is
// the bare value, without the 0x40 content bit the wire sets on a tag that has
// content.
type Tag uint16

// Page reports the code page the tag belongs to.
func (t Tag) Page() byte { return byte(t >> 8) }

// Token reports the tag token within its code page.
func (t Tag) Token() byte { return byte(t) }

// ActiveSync code pages (MS-ASWBXML). The page byte follows a SWITCH_PAGE.
const (
	PageAirSync         = 0x00
	PageContacts        = 0x01
	PageEmail           = 0x02
	PageCalendar        = 0x04
	PageMove            = 0x05
	PageGetItemEstimate = 0x06
	PageFolderHierarchy = 0x07
	PageMeetingResponse = 0x08
	PageTasks           = 0x09
	PageResolveRecips   = 0x0A
	PageValidateCert    = 0x0B
	PagePing            = 0x0D
	PageProvision       = 0x0E
	PageSearch          = 0x0F
	PageGAL             = 0x10
	PageAirSyncBase     = 0x11
	PageSettings        = 0x12
	PageItemOperations  = 0x14
	PageComposeMail     = 0x15
	PageNotes           = 0x17
)

// AirSync (code page 0x00).
const (
	ASSync             Tag = 0x0005
	ASResponses        Tag = 0x0006
	ASAdd              Tag = 0x0007
	ASChange           Tag = 0x0008
	ASDelete           Tag = 0x0009
	ASFetch            Tag = 0x000A
	ASSyncKey          Tag = 0x000B
	ASClientID         Tag = 0x000C
	ASServerID         Tag = 0x000D
	ASStatus           Tag = 0x000E
	ASCollection       Tag = 0x000F
	ASClass            Tag = 0x0010
	ASCollectionID     Tag = 0x0012
	ASGetChanges       Tag = 0x0013
	ASMoreAvailable    Tag = 0x0014
	ASWindowSize       Tag = 0x0015
	ASCommands         Tag = 0x0016
	ASOptions          Tag = 0x0017
	ASFilterType       Tag = 0x0018
	ASCollections      Tag = 0x001C
	ASData             Tag = 0x001D
	ASSupported        Tag = 0x0020
	ASSoftDelete       Tag = 0x0021
	ASMIMESupport      Tag = 0x0022
	ASMIMETruncation   Tag = 0x0023
	ASLimit            Tag = 0x0025 // Sync hanging-Sync Status 14 limit (Since 12.1)
	ASConversationMode Tag = 0x0027
	ASMaxItems         Tag = 0x0028
	ASHeartbeatInt     Tag = 0x0029
)

// Contacts (code page 0x01, MS-ASCONTACTS). The full field set; the 2.5 inline
// Body/BodySize/BodyTruncated/Rtf tokens are carried under AirSyncBase in 12.0+, so
// only the contact data fields are defined here.
const (
	CAnniversary          Tag = 0x0105
	CAssistantName        Tag = 0x0106
	CAssistantPhoneNumber Tag = 0x0107
	CBirthday             Tag = 0x0108
	CBusiness2PhoneNumber Tag = 0x010C
	CBusinessCity         Tag = 0x010D
	CBusinessCountry      Tag = 0x010E
	CBusinessPostalCode   Tag = 0x010F
	CBusinessState        Tag = 0x0110
	CBusinessStreet       Tag = 0x0111
	CBusinessFaxNumber    Tag = 0x0112
	CBusinessPhoneNumber  Tag = 0x0113
	CCarPhoneNumber       Tag = 0x0114
	CCategories           Tag = 0x0115
	CCategory             Tag = 0x0116
	CChildren             Tag = 0x0117
	CChild                Tag = 0x0118
	CCompanyName          Tag = 0x0119
	CDepartment           Tag = 0x011A
	CEmail1Address        Tag = 0x011B
	CEmail2Address        Tag = 0x011C
	CEmail3Address        Tag = 0x011D
	CFileAs               Tag = 0x011E
	CFirstName            Tag = 0x011F
	CHome2PhoneNumber     Tag = 0x0120
	CHomeCity             Tag = 0x0121
	CHomeCountry          Tag = 0x0122
	CHomePostalCode       Tag = 0x0123
	CHomeState            Tag = 0x0124
	CHomeStreet           Tag = 0x0125
	CHomeFaxNumber        Tag = 0x0126
	CHomePhoneNumber      Tag = 0x0127
	CJobTitle             Tag = 0x0128
	CLastName             Tag = 0x0129
	CMiddleName           Tag = 0x012A
	CMobilePhoneNumber    Tag = 0x012B
	COfficeLocation       Tag = 0x012C
	COtherCity            Tag = 0x012D
	COtherCountry         Tag = 0x012E
	COtherPostalCode      Tag = 0x012F
	COtherState           Tag = 0x0130
	COtherStreet          Tag = 0x0131
	CPagerNumber          Tag = 0x0132
	CRadioPhoneNumber     Tag = 0x0133
	CSpouse               Tag = 0x0134
	CSuffix               Tag = 0x0135
	CTitle                Tag = 0x0136
	CWebPage              Tag = 0x0137
	CYomiCompanyName      Tag = 0x0138
	CYomiFirstName        Tag = 0x0139
	CYomiLastName         Tag = 0x013A
	CPicture              Tag = 0x013C
	CAlias                Tag = 0x013D
	CWeightedRank         Tag = 0x013E
)

// Tasks (code page 0x09, MS-ASTASK). The full field set; the recurrence sub-fields
// (Recurrence..DeadOccur, OrdinalDate..FirstDayOfWeek) are defined so the codec
// round-trips them, but their mapping is deferred. The 2.5 inline Body tokens are
// carried under AirSyncBase in 12.0+.
const (
	TKCategories       Tag = 0x0908
	TKCategory         Tag = 0x0909
	TKComplete         Tag = 0x090A
	TKDateCompleted    Tag = 0x090B
	TKDueDate          Tag = 0x090C
	TKUtcDueDate       Tag = 0x090D
	TKImportance       Tag = 0x090E
	TKRecurrence       Tag = 0x090F
	TKRecurType        Tag = 0x0910
	TKRecurStart       Tag = 0x0911
	TKRecurUntil       Tag = 0x0912
	TKRecurOccurrences Tag = 0x0913
	TKRecurInterval    Tag = 0x0914
	TKRecurDayOfMonth  Tag = 0x0915
	TKRecurDayOfWeek   Tag = 0x0916
	TKRecurWeekOfMonth Tag = 0x0917
	TKRecurMonthOfYear Tag = 0x0918
	TKRecurRegenerate  Tag = 0x0919
	TKRecurDeadOccur   Tag = 0x091A
	TKReminderSet      Tag = 0x091B
	TKReminderTime     Tag = 0x091C
	TKSensitivity      Tag = 0x091D
	TKStartDate        Tag = 0x091E
	TKUtcStartDate     Tag = 0x091F
	TKSubject          Tag = 0x0920
	TKOrdinalDate      Tag = 0x0922
	TKSubOrdinalDate   Tag = 0x0923
	TKCalendarType     Tag = 0x0924
	TKIsLeapMonth      Tag = 0x0925
	TKFirstDayOfWeek   Tag = 0x0926
)

// Notes (code page 0x17, MS-ASNOTE, since 14.0). The note body is carried under
// AirSyncBase.
const (
	NTSubject      Tag = 0x1705
	NTMessageClass Tag = 0x1706
	NTLastModified Tag = 0x1707
	NTCategories   Tag = 0x1708
	NTCategory     Tag = 0x1709
)

// Email (code page 0x02). The pre-12.0 Attachment/Body tokens are omitted; 12.0+
// carries the body and attachments under AirSyncBase.
const (
	EMDateReceived Tag = 0x020F
	EMDisplayTo    Tag = 0x0211
	EMImportance   Tag = 0x0212
	EMMessageClass Tag = 0x0213
	EMSubject      Tag = 0x0214
	EMRead         Tag = 0x0215
	EMTo           Tag = 0x0216
	EMCc           Tag = 0x0217
	EMFrom         Tag = 0x0218
	EMReplyTo      Tag = 0x0219
	EMThreadTopic  Tag = 0x0235
	EMInternetCPID Tag = 0x0239
	EMFlag         Tag = 0x023A
	EMFlagStatus   Tag = 0x023B
	EMContentClass Tag = 0x023C
)

// Calendar (code page 0x04, MS-ASCAL). The deprecated 2.5 RTF/Body/inline-
// attachment tokens are omitted; 12.0+ carries the body under AirSyncBase.
const (
	CalTimezone             Tag = 0x0405
	CalAllDayEvent          Tag = 0x0406
	CalAttendees            Tag = 0x0407
	CalAttendee             Tag = 0x0408
	CalEmail                Tag = 0x0409
	CalName                 Tag = 0x040A
	CalBusyStatus           Tag = 0x040D
	CalCategories           Tag = 0x040E
	CalCategory             Tag = 0x040F
	CalDtStamp              Tag = 0x0411
	CalEndTime              Tag = 0x0412
	CalException            Tag = 0x0413
	CalExceptions           Tag = 0x0414
	CalDeleted              Tag = 0x0415
	CalExceptionStartTime   Tag = 0x0416
	CalLocation             Tag = 0x0417
	CalMeetingStatus        Tag = 0x0418
	CalOrganizerEmail       Tag = 0x0419
	CalOrganizerName        Tag = 0x041A
	CalRecurrence           Tag = 0x041B
	CalType                 Tag = 0x041C
	CalUntil                Tag = 0x041D
	CalOccurrences          Tag = 0x041E
	CalInterval             Tag = 0x041F
	CalDayOfWeek            Tag = 0x0420
	CalDayOfMonth           Tag = 0x0421
	CalWeekOfMonth          Tag = 0x0422
	CalMonthOfYear          Tag = 0x0423
	CalReminder             Tag = 0x0424
	CalSensitivity          Tag = 0x0425
	CalSubject              Tag = 0x0426
	CalStartTime            Tag = 0x0427
	CalUID                  Tag = 0x0428
	CalAttendeeStatus       Tag = 0x0429
	CalAttendeeType         Tag = 0x042A
	CalResponseRequested    Tag = 0x0434
	CalAppointmentReplyTime Tag = 0x0435
	CalResponseType         Tag = 0x0436
)

// MeetingResponse (code page 0x08, MS-ASCMD). The 16.x ProposedStart/EndTime and
// the deprecated 2.0 Version tokens are omitted.
const (
	MRCalendarID      Tag = 0x0805
	MRFolderID        Tag = 0x0806
	MRMeetingResponse Tag = 0x0807
	MRRequestID       Tag = 0x0808
	MRRequest         Tag = 0x0809
	MRResult          Tag = 0x080A
	MRStatus          Tag = 0x080B
	MRUserResponse    Tag = 0x080C
	MRInstanceID      Tag = 0x080E
	MRSendResponse    Tag = 0x0812
)

// GetItemEstimate (code page 0x06).
const (
	GIEGetItemEstimate Tag = 0x0605
	GIECollections     Tag = 0x0607
	GIECollection      Tag = 0x0608
	GIEClass           Tag = 0x0609
	GIECollectionID    Tag = 0x060A
	GIEEstimate        Tag = 0x060C
	GIEResponse        Tag = 0x060D
	GIEStatus          Tag = 0x060E
)

// FolderHierarchy (code page 0x07).
const (
	FHDisplayName  Tag = 0x0707
	FHServerID     Tag = 0x0708
	FHParentID     Tag = 0x0709
	FHType         Tag = 0x070A
	FHStatus       Tag = 0x070C
	FHChanges      Tag = 0x070E
	FHAdd          Tag = 0x070F
	FHDelete       Tag = 0x0710
	FHUpdate       Tag = 0x0711
	FHSyncKey      Tag = 0x0712
	FHFolderCreate Tag = 0x0713
	FHFolderDelete Tag = 0x0714
	FHFolderUpdate Tag = 0x0715
	FHFolderSync   Tag = 0x0716
	FHCount        Tag = 0x0717
)

// ResolveRecipients (code page 0x0A). v1 carries the GAL-resolution subset; the
// certificate, availability, and picture tokens are not yet served.
const (
	RRResolveRecipients Tag = 0x0A05
	RRResponse          Tag = 0x0A06
	RRStatus            Tag = 0x0A07
	RRType              Tag = 0x0A08
	RRRecipient         Tag = 0x0A09
	RRDisplayName       Tag = 0x0A0A
	RREmailAddress      Tag = 0x0A0B
	RROptions           Tag = 0x0A0F
	RRTo                Tag = 0x0A10
	RRRecipientCount    Tag = 0x0A12
	RRPicture           Tag = 0x0A18 // since 14.1: a recipient's portrait
	RRMaxSize           Tag = 0x0A19 // request: cap on portrait byte size
	RRData              Tag = 0x0A1A // response: base64 portrait bytes
	RRMaxPictures       Tag = 0x0A1B // request: cap on portraits returned
)

// ValidateCert (code page 0x0B) — S/MIME certificate validation.
const (
	VCValidateCert     Tag = 0x0B05
	VCCertificates     Tag = 0x0B06
	VCCertificate      Tag = 0x0B07
	VCCertificateChain Tag = 0x0B08
	VCCheckCRL         Tag = 0x0B09
	VCStatus           Tag = 0x0B0A
)

// Ping (code page 0x0D).
const (
	PGPing         Tag = 0x0D05
	PGStatus       Tag = 0x0D07
	PGHeartbeatInt Tag = 0x0D08
	PGFolders      Tag = 0x0D09
	PGFolder       Tag = 0x0D0A
	PGID           Tag = 0x0D0B
	PGClass        Tag = 0x0D0C
	PGMaxFolders   Tag = 0x0D0D
)

// Provision (code page 0x0E). The handshake tokens plus the full EASProvisionDoc
// policy-detail set ([MS-ASWBXML] 2.1.2.1.16 / [MS-ASPROV] 2.2.2.44), so the server
// can serve a complete device policy, not only the permissive default.
const (
	PVProvision       Tag = 0x0E05
	PVPolicies        Tag = 0x0E06
	PVPolicy          Tag = 0x0E07
	PVPolicyType      Tag = 0x0E08
	PVPolicyKey       Tag = 0x0E09
	PVData            Tag = 0x0E0A
	PVStatus          Tag = 0x0E0B
	PVRemoteWipe      Tag = 0x0E0C
	PVEASProvisionDoc Tag = 0x0E0D
	// EASProvisionDoc policy-detail tokens. Each maps a SyncPolicy field to its wire
	// element; token 0x12 (DocumentBrowseEnabled) is deprecated and intentionally
	// omitted. Token 0x10 is RequireStorageCardEncryption (the renamed
	// DeviceEncryptionEnabled).
	PVDevicePasswordEnabled                    Tag = 0x0E0E
	PVAlphanumericDevicePasswordRequired       Tag = 0x0E0F
	PVRequireStorageCardEncryption             Tag = 0x0E10
	PVPasswordRecoveryEnabled                  Tag = 0x0E11
	PVAttachmentsEnabled                       Tag = 0x0E13
	PVMinDevicePasswordLength                  Tag = 0x0E14
	PVMaxInactivityTimeDeviceLock              Tag = 0x0E15
	PVMaxDevicePasswordFailedAttempts          Tag = 0x0E16
	PVMaxAttachmentSize                        Tag = 0x0E17
	PVAllowSimpleDevicePassword                Tag = 0x0E18
	PVDevicePasswordExpiration                 Tag = 0x0E19
	PVDevicePasswordHistory                    Tag = 0x0E1A
	PVAllowStorageCard                         Tag = 0x0E1B
	PVAllowCamera                              Tag = 0x0E1C
	PVRequireDeviceEncryption                  Tag = 0x0E1D
	PVAllowUnsignedApplications                Tag = 0x0E1E
	PVAllowUnsignedInstallationPackages        Tag = 0x0E1F
	PVMinDevicePasswordComplexCharacters       Tag = 0x0E20
	PVAllowWiFi                                Tag = 0x0E21
	PVAllowTextMessaging                       Tag = 0x0E22
	PVAllowPOPIMAPEmail                        Tag = 0x0E23
	PVAllowBluetooth                           Tag = 0x0E24
	PVAllowIrDA                                Tag = 0x0E25
	PVRequireManualSyncWhenRoaming             Tag = 0x0E26
	PVAllowDesktopSync                         Tag = 0x0E27
	PVMaxCalendarAgeFilter                     Tag = 0x0E28
	PVAllowHTMLEmail                           Tag = 0x0E29
	PVMaxEmailAgeFilter                        Tag = 0x0E2A
	PVMaxEmailBodyTruncationSize               Tag = 0x0E2B
	PVMaxEmailHTMLBodyTruncationSize           Tag = 0x0E2C
	PVRequireSignedSMIMEMessages               Tag = 0x0E2D
	PVRequireEncryptedSMIMEMessages            Tag = 0x0E2E
	PVRequireSignedSMIMEAlgorithm              Tag = 0x0E2F
	PVRequireEncryptionSMIMEAlgorithm          Tag = 0x0E30
	PVAllowSMIMEEncryptionAlgorithmNegotiation Tag = 0x0E31
	PVAllowSMIMESoftCerts                      Tag = 0x0E32
	PVAllowBrowser                             Tag = 0x0E33
	PVAllowConsumerEmail                       Tag = 0x0E34
	PVAllowRemoteDesktop                       Tag = 0x0E35
	PVAllowInternetSharing                     Tag = 0x0E36
	PVUnapprovedInROMApplicationList           Tag = 0x0E37
	PVApplicationName                          Tag = 0x0E38
	PVApprovedApplicationList                  Tag = 0x0E39
	PVHash                                     Tag = 0x0E3A
	// PVAccountOnlyRemoteWipe (since EAS 16.1) signals a wipe that removes only the
	// account from the device, not a full device reset.
	PVAccountOnlyRemoteWipe Tag = 0x0E3B
)

// Search (code page 0x0F). v1 serves the GAL-store subset; the mailbox-query
// operators (Or/GreaterThan/LessThan/…) are not modeled; And/FreeText carry the
// mailbox query, LongId identifies a mailbox hit, DeepTraversal widens the scan.
const (
	SRSearch        Tag = 0x0F05
	SRStore         Tag = 0x0F07
	SRName          Tag = 0x0F08
	SRQuery         Tag = 0x0F09
	SROptions       Tag = 0x0F0A
	SRRange         Tag = 0x0F0B
	SRStatus        Tag = 0x0F0C
	SRResponse      Tag = 0x0F0D
	SRResult        Tag = 0x0F0E
	SRProperties    Tag = 0x0F0F
	SRTotal         Tag = 0x0F10
	SRAnd           Tag = 0x0F13
	SRFreeText      Tag = 0x0F15
	SRDeepTraversal Tag = 0x0F17
	SRLongId        Tag = 0x0F18
)

// GAL (code page 0x10) — the address-book properties a Search result carries.
// v1 populates the display name and address; the GALEntry model holds no other
// fields (phone, office, title, …). FirstName/LastName are emitted empty because
// some clients require the elements to be present to render an entry at all.
const (
	GALDisplayName  Tag = 0x1005
	GALFirstName    Tag = 0x100B
	GALLastName     Tag = 0x100C
	GALEmailAddress Tag = 0x100F
)

// AirSyncBase (code page 0x11).
const (
	ABBodyPreference    Tag = 0x1105
	ABType              Tag = 0x1106
	ABTruncationSize    Tag = 0x1107
	ABAllOrNone         Tag = 0x1108
	ABBody              Tag = 0x110A
	ABData              Tag = 0x110B
	ABEstimatedDataSize Tag = 0x110C
	ABTruncated         Tag = 0x110D
	ABAttachments       Tag = 0x110E
	ABAttachment        Tag = 0x110F
	ABAttDisplayName    Tag = 0x1110
	ABFileReference     Tag = 0x1111
	ABMethod            Tag = 0x1112
	ABNativeBodyType    Tag = 0x1116
	ABContentType       Tag = 0x1117
	ABPreview           Tag = 0x1118
)

// Settings (code page 0x12).
const (
	STSettings                 Tag = 0x1205
	STStatus                   Tag = 0x1206
	STGet                      Tag = 0x1207
	STSet                      Tag = 0x1208
	STOof                      Tag = 0x1209
	STOofState                 Tag = 0x120A
	STStartTime                Tag = 0x120B
	STEndTime                  Tag = 0x120C
	STOofMessage               Tag = 0x120D
	STAppliesToInternal        Tag = 0x120E
	STAppliesToExternalKnown   Tag = 0x120F
	STAppliesToExternalUnknown Tag = 0x1210
	STEnabled                  Tag = 0x1211
	STReplyMessage             Tag = 0x1212
	STBodyType                 Tag = 0x1213
	STDevicePassword           Tag = 0x1214
	STDeviceInformation        Tag = 0x1216
	STModel                    Tag = 0x1217
	STUserInformation          Tag = 0x121D
	STEmailAddresses           Tag = 0x121E
	STSmtpAddress              Tag = 0x121F
	STPrimarySmtpAddr          Tag = 0x1223
	STAccounts                 Tag = 0x1224
	STAccount                  Tag = 0x1225
)

// ComposeMail (code page 0x15, ActiveSync 14.0+).
const (
	CMSendMail        Tag = 0x1505
	CMSmartForward    Tag = 0x1506
	CMSmartReply      Tag = 0x1507
	CMSaveInSentItems Tag = 0x1508
	CMReplaceMime     Tag = 0x1509
	CMSource          Tag = 0x150B
	CMFolderID        Tag = 0x150C
	CMItemID          Tag = 0x150D
	CMLongId          Tag = 0x150E
	CMMIME            Tag = 0x1510
	CMClientID        Tag = 0x1511
	CMStatus          Tag = 0x1512
	CMAccountID       Tag = 0x1513
)

// Move (code page 0x05) — the MoveItems command.
const (
	MOMoves    Tag = 0x0505
	MOMove     Tag = 0x0506
	MOSrcMsgId Tag = 0x0507
	MOSrcFldId Tag = 0x0508
	MODstFldId Tag = 0x0509
	MOResponse Tag = 0x050A
	MOStatus   Tag = 0x050B
	MODstMsgId Tag = 0x050C
)

// ItemOperations (code page 0x14, ActiveSync 12.0+).
const (
	IOItemOperations      Tag = 0x1405
	IOFetch               Tag = 0x1406
	IOStore               Tag = 0x1407
	IOOptions             Tag = 0x1408
	IOProperties          Tag = 0x140B
	IOData                Tag = 0x140C
	IOStatus              Tag = 0x140D
	IOResponse            Tag = 0x140E
	IOEmptyFolderContents Tag = 0x1412
	IODeleteSubFolders    Tag = 0x1413
)

// Email2 codepage (0x16), MS-ASEMAIL/MS-ASWBXML (Since 14.0): the conversation
// grouping a thread-aware client renders. ConversationId groups a thread;
// ConversationIndex orders within it.
const (
	EM2ConversationId    Tag = 0x1609
	EM2ConversationIndex Tag = 0x160A
)

// Find command codepage (0x19), MS-ASCMD/MS-ASWBXML (Since 16.1): the unified
// mailbox/GAL search a 16.x client issues. The result carries the matched item's
// class, server id, and folder id from the AirSync codepage, plus the Find
// Properties holding the email render.
const (
	FNDFind                   Tag = 0x1905
	FNDSearchId               Tag = 0x1906
	FNDExecuteSearch          Tag = 0x1907
	FNDMailboxSearchCriterion Tag = 0x1908
	FNDQuery                  Tag = 0x1909
	FNDStatus                 Tag = 0x190A
	FNDFreeText               Tag = 0x190B
	FNDOptions                Tag = 0x190C
	FNDRange                  Tag = 0x190D
	FNDDeepTraversal          Tag = 0x190E
	FNDResponse               Tag = 0x1911
	FNDResult                 Tag = 0x1912
	FNDProperties             Tag = 0x1913
	FNDTotal                  Tag = 0x1916
	FNDGalSearchCriterion     Tag = 0x1919
)
