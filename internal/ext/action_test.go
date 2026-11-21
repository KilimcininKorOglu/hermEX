package ext

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"hermex/internal/mapi"
)

func roundTripRuleActions(t *testing.T, r mapi.RuleActions) {
	t.Helper()
	p := NewPush(0)
	if err := p.RuleActions(r); err != nil {
		t.Fatalf("push: %v", err)
	}
	got, err := NewPull(p.Bytes(), 0).RuleActions()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", got, r)
	}
}

func TestActionBlockLengthPrefix(t *testing.T) {
	r := mapi.RuleActions{Blocks: []mapi.ActionBlock{
		{Type: mapi.OpBounce, Data: uint32(0x0000000D)},
	}}
	p := NewPush(0)
	if err := p.RuleActions(r); err != nil {
		t.Fatalf("push: %v", err)
	}
	b := p.Bytes()
	// count(2) then the block: length prefix counts type(1)+flavor(4)+flags(4)+
	// bounce(4) = 13.
	if b[0] != 0x01 || b[1] != 0x00 {
		t.Fatalf("count = % X, want 01 00", b[:2])
	}
	if b[2] != 0x0D || b[3] != 0x00 {
		t.Fatalf("block length = % X, want 0D 00", b[2:4])
	}
	if b[4] != mapi.OpBounce {
		t.Fatalf("type = %#x, want OpBounce", b[4])
	}
}

// TestRuleActionVectors pins each action's ActionData to [MS-OXORULE] 2.2.5.1:
// ActionType values (OP_MOVE 0x01, OP_REPLY 0x03, OP_DEFER_ACTION 0x05,
// OP_BOUNCE 0x06, OP_TAG 0x09, OP_DELETE 0x0A), the ActionBlock header
// (ActionLength 2, ActionType 1, ActionFlavor 4, ActionFlags 4), the reply
// template FID/MID/GUID, the standard move form (FolderInThisStore, StoreEIDSize,
// StoreEID, FolderEIDSize, ServerEid of Ours 0x01, FolderId 8, MessageId 8,
// Instance 4), and a Folder ID as ReplicaId (2) then GlobalCounter (6)
// ([MS-OXCDATA] 2.2.1.1). A round-trip alone accepts a layout both sides got wrong.
func TestRuleActionVectors(t *testing.T) {
	fid := mapi.MakeEID(1, mapi.GlobCnt{0, 0, 0, 0, 0, 0x0D})
	fidBytes := []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x0D}
	mid := mapi.MakeEID(1, mapi.GlobCnt{0, 0, 0, 0, 0, 0x2A})
	msgIDBytes := []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x2A}
	guid := sampleGUID()
	tag := mapi.MakeTag(0x1234, mapi.PtLong)
	header := func(length byte, op uint8, flavor, flags byte) []byte {
		return []byte{length, 0x00, op, flavor, 0x00, 0x00, 0x00, flags, 0x00, 0x00, 0x00}
	}
	join := func(parts ...[]byte) []byte {
		var b []byte
		for _, p := range parts {
			b = append(b, p...)
		}
		return b
	}
	cases := []struct {
		name  string
		block mapi.ActionBlock
		want  []byte
	}{
		{"bounce", mapi.ActionBlock{Type: mapi.OpBounce, Flags: 3, Data: uint32(0x26)},
			join(header(13, 0x06, 0, 3), []byte{0x26, 0x00, 0x00, 0x00})},
		{"delete", mapi.ActionBlock{Type: mapi.OpDelete}, header(9, 0x0A, 0, 0)},
		{"defer", mapi.ActionBlock{Type: mapi.OpDeferAction, Data: []byte{0xDE, 0xAD}},
			join(header(11, 0x05, 0, 0), []byte{0xDE, 0xAD})},
		{"tag", mapi.ActionBlock{Type: mapi.OpTag, Data: mapi.TaggedPropVal{Tag: tag, Value: int32(7)}},
			join(header(17, 0x09, 0, 0), []byte{0x03, 0x00, 0x34, 0x12, 0x07, 0x00, 0x00, 0x00})},
		{"reply", mapi.ActionBlock{Type: mapi.OpReply, Flavor: 2, Data: mapi.ReplyAction{
			TemplateFolderID: fid, TemplateMessageID: mid, TemplateGUID: guid}},
			join(header(9+32, 0x03, 2, 0), fidBytes, msgIDBytes, guidBytes(t, guid))},
		{"move", mapi.ActionBlock{Type: mapi.OpMove, Data: mapi.MoveCopyAction{
			SameStore: true, FolderEID: mapi.SVREID{FolderID: fid}}},
			join(header(9+1+3+2+21, 0x01, 0, 0), []byte{0x01, 0x01, 0x00, 0x00, 0x15, 0x00, 0x01},
				fidBytes, make([]byte, 12))},
	}
	for _, c := range cases {
		r := mapi.RuleActions{Blocks: []mapi.ActionBlock{c.block}}
		want := append([]byte{0x01, 0x00}, c.want...) // NoOfActions 1
		p := NewPush(0)
		if err := p.RuleActions(r); err != nil {
			t.Fatalf("%s push: %v", c.name, err)
		}
		if !bytes.Equal(p.Bytes(), want) {
			t.Errorf("%s bytes = % X, want % X", c.name, p.Bytes(), want)
		}
		got, err := NewPull(want, 0).RuleActions()
		if err != nil || !reflect.DeepEqual(got, r) {
			t.Errorf("%s pull of the spec bytes = %#v (err %v), want %#v", c.name, got, err, r)
		}
	}
}

// guidBytes encodes g the way [MS-OXCDATA] 2.5.1 lays out a GUID: Data1, Data2
// and Data3 little-endian, then Data4 as is. It reads the fields off the GUID
// value, so it is independent of the codec under test.
func guidBytes(t *testing.T, g mapi.GUID) []byte {
	t.Helper()
	b := []byte{byte(g.Data1), byte(g.Data1 >> 8), byte(g.Data1 >> 16), byte(g.Data1 >> 24),
		byte(g.Data2), byte(g.Data2 >> 8), byte(g.Data3), byte(g.Data3 >> 8)}
	return append(b, g.Data4[:]...)
}

func TestRuleActionsSimpleTypes(t *testing.T) {
	tag := mapi.MakeTag(0x1234, mapi.PtLong)
	roundTripRuleActions(t, mapi.RuleActions{Blocks: []mapi.ActionBlock{
		{Type: mapi.OpTag, Flavor: 0, Flags: 0, Data: mapi.TaggedPropVal{Tag: tag, Value: int32(7)}},
		{Type: mapi.OpBounce, Data: uint32(0x0000000D)},
		{Type: mapi.OpDelete, Data: nil},
		{Type: mapi.OpMarkAsRead, Data: nil},
		{Type: mapi.OpDeferAction, Data: []byte{0xDE, 0xAD, 0xBE, 0xEF}},
	}})
}

func TestRuleActionsReply(t *testing.T) {
	roundTripRuleActions(t, mapi.RuleActions{Blocks: []mapi.ActionBlock{
		{Type: mapi.OpReply, Flavor: 0x2, Flags: 0, Data: mapi.ReplyAction{
			TemplateFolderID:  mapi.MakeEID(1, mapi.GlobCnt{0, 0, 0, 0, 0, 0x0D}),
			TemplateMessageID: mapi.MakeEID(1, mapi.GlobCnt{0, 0, 0, 0, 0, 0x2A}),
			TemplateGUID:      sampleGUID(),
		}},
	}})
}

func TestRuleActionsForward(t *testing.T) {
	rb := mapi.RecipientBlock{PropVals: []mapi.TaggedPropVal{
		{Tag: mapi.MakeTag(0x0C15, mapi.PtLong), Value: int32(1)},
		{Tag: mapi.MakeTag(0x3001, mapi.PtUnicode), Value: "Recipient"},
	}}
	roundTripRuleActions(t, mapi.RuleActions{Blocks: []mapi.ActionBlock{
		{Type: mapi.OpForward, Flavor: 0x1, Flags: 0, Data: mapi.ForwardDelegateAction{
			Recipients: []mapi.RecipientBlock{rb},
		}},
	}})
}

func TestRuleActionsMoveCopySameStore(t *testing.T) {
	roundTripRuleActions(t, mapi.RuleActions{Blocks: []mapi.ActionBlock{
		{Type: mapi.OpMove, Data: mapi.MoveCopyAction{
			SameStore: true,
			FolderEID: mapi.SVREID{
				FolderID:  mapi.MakeEID(1, mapi.GlobCnt{0, 0, 0, 0, 0, 0x0D}),
				MessageID: mapi.MakeEID(1, mapi.GlobCnt{0, 0, 0, 0, 0, 0x01}),
				Instance:  0,
			},
		}},
	}})
}

func TestRuleActionsMoveCopyCrossStore(t *testing.T) {
	roundTripRuleActions(t, mapi.RuleActions{Blocks: []mapi.ActionBlock{
		{Type: mapi.OpCopy, Data: mapi.MoveCopyAction{
			SameStore: false,
			StoreEID: &mapi.StoreEntryID{
				WrappedProviderUID: mapi.MuidStorePrivate,
				WrappedType:        0x0000000C,
				ServerName:         "srv",
				MailboxDN:          "/o=hermex/cn=u",
			},
			FolderEID: []byte{0x01, 0x02, 0x03, 0x04},
		}},
	}})
}

func TestRuleActionsEmptyRejected(t *testing.T) {
	// Push rejects an empty action list.
	if err := NewPush(0).RuleActions(mapi.RuleActions{}); !errors.Is(err, ErrFormat) {
		t.Fatalf("push empty err = %v, want ErrFormat", err)
	}
	// Pull rejects a zero block count.
	p := NewPush(0)
	p.Uint16(0)
	if _, err := NewPull(p.Bytes(), 0).RuleActions(); !errors.Is(err, ErrFormat) {
		t.Fatalf("pull zero-count err = %v, want ErrFormat", err)
	}
}

func TestRuleActionsPropValueDispatch(t *testing.T) {
	r := mapi.RuleActions{Blocks: []mapi.ActionBlock{{Type: mapi.OpDelete}}}
	p := NewPush(0)
	if err := p.PropValue(mapi.PtActions, r); err != nil {
		t.Fatalf("push: %v", err)
	}
	v, err := NewPull(p.Bytes(), 0).PropValue(mapi.PtActions)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !reflect.DeepEqual(v, r) {
		t.Fatalf("dispatch round-trip = %#v, want %#v", v, r)
	}
}

// nestedTagActions builds a rule action list whose OP_TAG value is another action
// list, depth levels deep, ending in an OP_TAG that sets a PtLong.
func nestedTagActions(depth int) []byte {
	var b []byte
	block := func(tag uint32) {
		b = binary.LittleEndian.AppendUint16(b, 1)    // block count
		b = binary.LittleEndian.AppendUint16(b, 0x11) // block length
		b = append(b, mapi.OpTag)
		b = binary.LittleEndian.AppendUint32(b, 0) // flavor
		b = binary.LittleEndian.AppendUint32(b, 0) // flags
		b = binary.LittleEndian.AppendUint32(b, tag)
	}
	for range depth {
		block(uint32(mapi.PtActions) | 0x0001_0000)
	}
	block(uint32(mapi.PtLong) | 0x0002_0000)
	return binary.LittleEndian.AppendUint32(b, 7)
}

// TestRuleActionsRejectsSelfNesting is the stack-overflow guard: an OP_TAG action
// whose value is itself an action list makes the reader recurse once per 17
// bytes, and a stack overflow is a runtime throw the daemon cannot recover from,
// so a list nested past the cap is refused.
func TestRuleActionsRejectsSelfNesting(t *testing.T) {
	_, err := NewPull(nestedTagActions(maxActionsDepth+50), 0).RuleActions()
	if !errors.Is(err, errActionsTooDeep) {
		t.Fatalf("self-nesting action list gave %v, want the depth-limit refusal", err)
	}
}

// TestRuleActionsKeepsShallowNesting is the control: a list nested below the cap
// still decodes, so the guard refuses only the attack.
func TestRuleActionsKeepsShallowNesting(t *testing.T) {
	if _, err := NewPull(nestedTagActions(3), 0).RuleActions(); err != nil {
		t.Fatalf("a shallow nested action list was refused: %v", err)
	}
}
