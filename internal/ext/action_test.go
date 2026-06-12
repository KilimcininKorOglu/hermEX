package ext

import (
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
