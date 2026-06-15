package ics

// FastTransfer stream markers ([MS-OXCFXICS] 2.2.4.1). Each is a 4-byte
// little-endian word the parser recognises as a structural marker (carrying no
// value body) before falling back to reading the word as a property tag.
const (
	markerStartTopFld            uint32 = 0x40090003
	markerStartSubFld            uint32 = 0x400A0003
	markerEndFolder              uint32 = 0x400B0003
	markerStartMessage           uint32 = 0x400C0003
	markerEndMessage             uint32 = 0x400D0003
	markerStartFAIMsg            uint32 = 0x40100003
	markerStartEmbed             uint32 = 0x40010003
	markerEndEmbed               uint32 = 0x40020003
	markerStartRecip             uint32 = 0x40030003
	markerEndToRecip             uint32 = 0x40040003
	markerNewAttach              uint32 = 0x40000003
	markerEndAttach              uint32 = 0x400E0003
	markerIncrSyncChg            uint32 = 0x40120003
	markerIncrSyncChgPartial     uint32 = 0x407D0003
	markerIncrSyncDel            uint32 = 0x40130003
	markerIncrSyncEnd            uint32 = 0x40140003
	markerIncrSyncRead           uint32 = 0x402F0003
	markerIncrSyncStateBegin     uint32 = 0x403A0003
	markerIncrSyncStateEnd       uint32 = 0x403B0003
	markerIncrSyncProgressMode   uint32 = 0x4074000B
	markerIncrSyncProgressPerMsg uint32 = 0x4075000B
	markerIncrSyncMessage        uint32 = 0x40150003
	markerIncrSyncGroupInfo      uint32 = 0x407B0102
	markerFXErrorInfo            uint32 = 0x40180003
)

// markerSet is the exact set of words a reader treats as structural markers
// ([MS-OXCFXICS] 2.2.4.1.x). A 4-byte word NOT in this set is a property tag
// (propdef) introducing a value.
var markerSet = map[uint32]struct{}{
	markerStartTopFld: {}, markerStartSubFld: {}, markerEndFolder: {},
	markerStartMessage: {}, markerEndMessage: {}, markerStartFAIMsg: {},
	markerStartEmbed: {}, markerEndEmbed: {}, markerStartRecip: {}, markerEndToRecip: {},
	markerNewAttach: {}, markerEndAttach: {},
	markerIncrSyncChg: {}, markerIncrSyncChgPartial: {}, markerIncrSyncDel: {},
	markerIncrSyncEnd: {}, markerIncrSyncRead: {}, markerIncrSyncStateBegin: {},
	markerIncrSyncStateEnd: {}, markerIncrSyncProgressMode: {}, markerIncrSyncProgressPerMsg: {},
	markerIncrSyncMessage: {}, markerIncrSyncGroupInfo: {}, markerFXErrorInfo: {},
}

func isMarker(w uint32) bool { _, ok := markerSet[w]; return ok }

// metaTagIdsetGiven is the one meta-tag whose on-wire type field lies: the
// propdef type is PT_LONG (0x40170003) but the value body is PT_BINARY. A reader
// keys on the literal word and reads the body as binary, retagging to the honest
// PT_BINARY form (0x40170102). ([MS-OXCFXICS] 3.2.5.2.1.)
const metaTagIdsetGiven uint32 = 0x40170003

// State meta-tags ([MS-OXCFXICS] 2.2.1.1). The given/seen idsets an ics State
// serialises ride the stream under these PT_BINARY tags. metaTagIdsetGiven1 is
// the honest PT_BINARY form a producer emits for the given set (vs the lying
// PT_LONG metaTagIdsetGiven a reader must also accept).
const (
	metaTagIdsetGiven1  uint32 = 0x40170102
	metaTagCnsetSeen    uint32 = 0x67960102
	metaTagCnsetSeenFAI uint32 = 0x67DA0102
	metaTagCnsetRead    uint32 = 0x67D20102
)

// cpUTF16 is the code-page id (1200) meaning UTF-16LE in the FastTransfer
// code-page string flag (FXICS_CODEPAGE_FLAG = 0x8000).
const (
	cpUTF16          uint16 = 1200
	fxCodepageFlag   uint16 = 0x8000
	propIDMessageCls uint16 = 0x001A // PR_MESSAGE_CLASS — always written PT_STRING8
)
