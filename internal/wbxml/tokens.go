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
	PageEmail           = 0x02
	PageMove            = 0x05
	PageGetItemEstimate = 0x06
	PageFolderHierarchy = 0x07
	PagePing            = 0x0D
	PageProvision       = 0x0E
	PageAirSyncBase     = 0x11
	PageSettings        = 0x12
	PageItemOperations  = 0x14
	PageComposeMail     = 0x15
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
	ASConversationMode Tag = 0x0027
	ASMaxItems         Tag = 0x0028
	ASHeartbeatInt     Tag = 0x0029
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

// Provision (code page 0x0E). Only the handshake tokens are modeled; the policy
// detail tokens are not needed for the trivial issue-a-key flow.
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
	// PVDevicePasswordEnabled (0x0E) is the one policy-detail token v1 emits, set
	// to 0 for a permissive (no device password) policy.
	PVDevicePasswordEnabled Tag = 0x0E0E
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
	IOItemOperations Tag = 0x1405
	IOFetch          Tag = 0x1406
	IOStore          Tag = 0x1407
	IOProperties     Tag = 0x140B
	IOStatus         Tag = 0x140D
	IOResponse       Tag = 0x140E
)
