package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildGetRulesTable builds a RopGetRulesTable request: RopId, LogonId,
// InputHandleIndex (the folder), OutputHandleIndex (the table), TableFlags.
func buildGetRulesTable(inIdx, outIdx, tableFlags uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetRulesTable)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(tableFlags)
	return b.Bytes()
}

// buildModifyRules builds a RopModifyRules request: RopId, LogonId, InputHandleIndex,
// ModifyRulesFlags, RulesCount, then each RuleData (row flags + tagged-propval array).
// A RuleData row has the same wire shape as a RopModifyPermissions PermissionData, so
// the permDataRow helper is reused.
func buildModifyRules(t *testing.T, inIdx, modifyFlags uint8, rows []permDataRow) []byte {
	t.Helper()
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropModifyRules)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(modifyFlags)
	b.Uint16(uint16(len(rows)))
	for _, r := range rows {
		b.Uint8(r.flags)
		b.Uint16(uint16(len(r.props)))
		for _, tp := range r.props {
			if err := b.TaggedPropVal(tp); err != nil {
				t.Fatalf("encode TaggedPropVal %#x: %v", uint32(tp.Tag), err)
			}
		}
	}
	return b.Bytes()
}

// rulesTableHandle parses a RopGetRulesTable response and asserts the bare-head
// contract: exactly RopId + HandleIndex + ReturnValue, no trailing RowCount (a phantom
// field would desync the rest of the ROP batch).
func rulesTableHandle(t *testing.T, resp []byte) {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropGetRulesTable {
		t.Fatalf("RopId = %#x, want GetRulesTable", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetRulesTable ReturnValue = %#x", ec)
	}
	if p.Remaining() != 0 {
		t.Fatalf("GetRulesTable response has %d trailing bytes; the contract is a bare head (no RowCount)", p.Remaining())
	}
}

// applyModifyRules dispatches a RopModifyRules batch and asserts the bare-head
// response whose HandleIndex echoes the input folder handle.
func applyModifyRules(t *testing.T, sess *Session, folderH uint32, modifyFlags uint8, rows []permDataRow) {
	t.Helper()
	resp, _ := sess.Dispatch(buildModifyRules(t, 0, modifyFlags, rows), []uint32{folderH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropModifyRules {
		t.Fatalf("RopId = %#x, want ModifyRules", id)
	}
	if h := mustU8(t, p, "hindex"); h != 0 {
		t.Errorf("ModifyRules HandleIndex = %d, want 0 (the input handle)", h)
	}
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("ModifyRules ReturnValue = %#x", ec)
	}
	if p.Remaining() != 0 {
		t.Fatalf("ModifyRules response has %d trailing bytes; the contract is a bare head", p.Remaining())
	}
}

// readRuleRows reads a folder's rules on an existing session+folder handle
// (GetRulesTable -> SetColumns -> QueryRows), indexed by PR_RULE_ID.
func readRuleRows(t *testing.T, sess *Session, folderH uint32) map[int64]mapi.PropertyValues {
	t.Helper()
	grt, h := sess.Dispatch(buildGetRulesTable(0, 1, 0), []uint32{folderH, 0xFFFFFFFF})
	rulesTableHandle(t, grt)
	tableH := h[1]

	cols := []mapi.PropTag{mapi.PrRuleID, mapi.PrRuleName, mapi.PrRuleState, mapi.PrRuleSequence}
	sess.Dispatch(buildSetColumns(0, cols), []uint32{tableH})
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 64), []uint32{tableH})

	_, rows := queryRowsResponse(t, qr, cols)
	out := make(map[int64]mapi.PropertyValues, len(rows))
	for _, r := range rows {
		id, _ := r.Get(mapi.PrRuleID)
		out[id.(int64)] = r
	}
	return out
}

// ruleAddRow is a RopModifyRules ADD row carrying a full rule: a ResContent condition
// on the subject paired with a mark-as-read action.
func ruleAddRow(name, needle string, state uint32, sequence int32) permDataRow {
	return permDataRow{
		flags: ruleRowAdd,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrRuleName, Value: name},
			{Tag: mapi.PrRuleState, Value: int32(state)},
			{Tag: mapi.PrRuleSequence, Value: sequence},
			{Tag: mapi.PrRuleCondition, Value: mapi.Restriction{
				Type: mapi.ResContent,
				Value: mapi.ContentRestriction{
					FuzzyLevel: 0x00010001, // FL_SUBSTRING | FL_IGNORECASE
					PropTag:    mapi.PrSubject,
					PropVal:    mapi.TaggedPropVal{Tag: mapi.PrSubject, Value: needle},
				},
			}},
			{Tag: mapi.PrRuleActions, Value: mapi.RuleActions{Blocks: []mapi.ActionBlock{{Type: mapi.OpMarkAsRead}}}},
		},
	}
}

// inboxRulesSession seeds a store and opens a session + Inbox handle to drive rule ROPs.
func inboxRulesSession(t *testing.T) (*Session, uint32) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return openFolderSession(t, dir, nil, mapi.PrivateFIDInbox)
}

// TestModifyRulesAddThenGetRulesTable is the end-to-end round-trip: an ADD through
// RopModifyRules must read back through RopGetRulesTable with its id, name, state, and
// sequence intact. This is the test that verifies the rule proptag HEX is correct end
// to end — a wrong id on any column would make that value fail to round-trip.
func TestModifyRulesAddThenGetRulesTable(t *testing.T) {
	sess, folderH := inboxRulesSession(t)

	applyModifyRules(t, sess, folderH, 0, []permDataRow{
		ruleAddRow("rop-rule", "invoice", mapi.RuleStateEnabled, 10),
	})

	rows := readRuleRows(t, sess, folderH)
	if len(rows) != 1 {
		t.Fatalf("GetRulesTable returned %d rules, want 1", len(rows))
	}
	for id, row := range rows {
		if id <= 0 {
			t.Errorf("PR_RULE_ID = %d, want a positive assigned id", id)
		}
		if name, _ := row.Get(mapi.PrRuleName); name != "rop-rule" {
			t.Errorf("PR_RULE_NAME = %v, want rop-rule", name)
		}
		if st, _ := row.Get(mapi.PrRuleState); st != int32(mapi.RuleStateEnabled) {
			t.Errorf("PR_RULE_STATE = %v, want ST_ENABLED", st)
		}
		if seq, _ := row.Get(mapi.PrRuleSequence); seq != int32(10) {
			t.Errorf("PR_RULE_SEQUENCE = %v, want 10", seq)
		}
	}
}

// TestModifyRulesRemoveThroughRop adds a rule, then removes it by its PR_RULE_ID
// through a second RopModifyRules, leaving the table empty.
func TestModifyRulesRemoveThroughRop(t *testing.T) {
	sess, folderH := inboxRulesSession(t)
	applyModifyRules(t, sess, folderH, 0, []permDataRow{ruleAddRow("doomed", "x", mapi.RuleStateEnabled, 1)})

	rows := readRuleRows(t, sess, folderH)
	if len(rows) != 1 {
		t.Fatalf("after add: %d rules, want 1", len(rows))
	}
	var id int64
	for k := range rows {
		id = k
	}

	applyModifyRules(t, sess, folderH, 0, []permDataRow{{
		flags: ruleRowRemove,
		props: []mapi.TaggedPropVal{{Tag: mapi.PrRuleID, Value: id}},
	}})
	if rows := readRuleRows(t, sess, folderH); len(rows) != 0 {
		t.Errorf("after remove: %d rules, want 0", len(rows))
	}
}

// TestModifyRulesBatchAlignment proves the RopModifyRules request decoder consumes its
// body exactly — ModifyRulesFlags, the count, and every RuleData with its
// TPROPVAL_ARRAY — so a following ROP in the same Execute batch parses from the right
// offset. The single-ROP tests pin the response framing; this pins the request side.
func TestModifyRulesBatchAlignment(t *testing.T) {
	sess, folderH := inboxRulesSession(t)

	batch := append(
		buildModifyRules(t, 0, 0, []permDataRow{ruleAddRow("batched", "receipt", mapi.RuleStateEnabled, 5)}),
		buildGetRulesTable(0, 1, 0)...,
	)
	resp, _ := sess.Dispatch(batch, []uint32{folderH, 0xFFFFFFFF})
	p := ext.NewPull(resp, ext.FlagUTF16)

	if id := mustU8(t, p, "RopId"); id != ropModifyRules {
		t.Fatalf("first RopId = %#x, want ModifyRules", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("ModifyRules ec = %#x", ec)
	}
	// The second response can only land here if the request decoder stayed aligned.
	if id := mustU8(t, p, "RopId"); id != ropGetRulesTable {
		t.Fatalf("second RopId = %#x, want GetRulesTable (request decoder misaligned)", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Errorf("GetRulesTable ec = %#x", ec)
	}
}
