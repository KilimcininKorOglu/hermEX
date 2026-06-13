package mapi

// Rule action types (MS-OXORULE §2.2.5.1.1 ActionType).
const (
	OpMove        uint8 = 0x01
	OpCopy        uint8 = 0x02
	OpReply       uint8 = 0x03
	OpOOFReply    uint8 = 0x04
	OpDeferAction uint8 = 0x05
	OpBounce      uint8 = 0x06
	OpForward     uint8 = 0x07
	OpDelegate    uint8 = 0x08
	OpTag         uint8 = 0x09
	OpDelete      uint8 = 0x0A
	OpMarkAsRead  uint8 = 0x0B
)

// Rule state flags (PidTagRuleState, MS-OXORULE §2.2.4). A rule's PR_RULE_STATE
// is a bitmask of these: it governs whether the rule runs, whether its last
// evaluation errored, and how it interacts with out-of-office processing.
const (
	RuleStateEnabled         uint32 = 0x00000001 // ST_ENABLED: the rule is active
	RuleStateError           uint32 = 0x00000002 // ST_ERROR: the last evaluation failed
	RuleStateOnlyWhenOOF     uint32 = 0x00000004 // ST_ONLY_WHEN_OOF: run only while out of office
	RuleStateKeepOOFHistory  uint32 = 0x00000008 // ST_KEEP_OOF_HIST: track OOF responses sent
	RuleStateExitLevel       uint32 = 0x00000010 // ST_EXIT_LEVEL: stop evaluating further rules after this one matches
	RuleStateSkipIfSCLTooLow uint32 = 0x00000020 // ST_SKIP_IF_SCL_IS_TOO_HIGH: skip when the spam confidence level is too high
	RuleStateClearOOFHistory uint32 = 0x80000000 // ST_CLEAR_OOF_HISTORY: clear the OOF response history
)

// RuleActions is the action list attached to a rule (MS-OXORULE §2.2.5). It must
// hold at least one block.
type RuleActions struct {
	Blocks []ActionBlock
}

// ActionBlock is one rule action (MS-OXORULE §2.2.5.1). The on-wire length
// prefix is computed during serialization, so it is not stored here. Data holds
// the per-type payload:
//
//	OpMove, OpCopy        MoveCopyAction
//	OpReply, OpOOFReply   ReplyAction
//	OpDeferAction         []byte (opaque)
//	OpBounce              uint32 (bounce code)
//	OpForward, OpDelegate ForwardDelegateAction
//	OpTag                 TaggedPropVal
//	OpDelete, OpMarkAsRead nil
type ActionBlock struct {
	Type   uint8
	Flavor uint32
	Flags  uint32
	Data   any
}

// MoveCopyAction moves or copies a message (MS-OXORULE §2.2.5.1.2.1). When
// SameStore is set, the target is in the same store: StoreEID is absent and
// FolderEID holds an SVREID. Otherwise StoreEID identifies the target store and
// FolderEID holds the raw folder entry-id bytes ([]byte).
type MoveCopyAction struct {
	SameStore bool
	StoreEID  *StoreEntryID
	FolderEID any // SVREID when SameStore, []byte otherwise
}

// ReplyAction replies with a template message (MS-OXORULE §2.2.5.1.2.2).
type ReplyAction struct {
	TemplateFolderID  EID
	TemplateMessageID EID
	TemplateGUID      GUID
}

// RecipientBlock is one recipient in a forward/delegate action
// (MS-OXORULE §2.2.5.1.2.5.1). It must hold at least one property value.
type RecipientBlock struct {
	PropVals []TaggedPropVal
}

// ForwardDelegateAction forwards or delegates to recipients
// (MS-OXORULE §2.2.5.1.2.5). It must hold at least one recipient.
type ForwardDelegateAction struct {
	Recipients []RecipientBlock
}
