package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ropGetRulesTable handles RopGetRulesTable ([MS-OXORULE] 2.2.1.2): it snapshots the
// folder's rules into a new table object whose rows the client reads with
// RopSetColumns/RopQueryRows. The response is the bare 6-byte head, no row count,
// matching the no-extra-body encoding, whose HandleIndex is the OUTPUT handle the
// table was allocated into (distinct from RopModifyRules, which echoes the input).
//
// The single defined TableFlags bit is Unicode (0x40); v1 serves PR_RULE_NAME /
// PR_RULE_PROVIDER as Unicode regardless, since the columns a client actually reads
// are its own RopSetColumns selection.
func (s *Session) ropGetRulesTable(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8() // OutputHandleIndex
	_, e2 := p.Uint8()       // TableFlags
	if e1 != nil || e2 != nil {
		return false
	}
	folder, ok := s.openFolder(out, ropGetRulesTable, handles, hindex, ohindex)
	if !ok {
		return true
	}
	// Opening the folder required only Visible; reading its rules requires ReadAny,
	// the same gate the contents table takes. A rule carries forward-to addresses,
	// block patterns and vacation text, so it is mailbox configuration a merely
	// visible folder must not hand over.
	if s.denyWrite(out, ropGetRulesTable, ohindex, folder.store, folder.folderID, mapi.FrightsReadAny) {
		return true
	}
	bags, err := ruleBags(folder.store, folder.folderID)
	if err != nil {
		writeErr(out, ropGetRulesTable, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{
		kind:  kindTable,
		store: folder.store,
		table: &tableState{kind: tableRules, rules: bags},
	})
	setHandle(handles, ohindex, h)

	out.Uint8(ropGetRulesTable)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}

// ruleBags builds one property bag per rule the table serves: PR_RULE_ID (the key a
// later Modify/Remove addresses), PR_RULE_SEQUENCE, PR_RULE_STATE, PR_RULE_NAME,
// PR_RULE_PROVIDER, and the rule's PR_RULE_CONDITION (RESTRICTION) and PR_RULE_ACTIONS
// (RULE_ACTIONS). A client's RopSetColumns picks which of these it actually reads; the
// condition/actions are only serialized when requested. (hermEX models these seven
// columns; the reference's level/user_flags/provider_data are not stored.)
func ruleBags(store *objectstore.Store, folderID int64) ([]mapi.PropertyValues, error) {
	rules, err := store.ListRules(folderID)
	if err != nil {
		return nil, err
	}
	bags := make([]mapi.PropertyValues, 0, len(rules))
	for _, r := range rules {
		var bag mapi.PropertyValues
		bag.Set(mapi.PrRuleID, r.ID)             // PtI8
		bag.Set(mapi.PrRuleSequence, r.Sequence) // PtLong
		// #nosec G115 -- the signed and unsigned views of the same 32 bits
		bag.Set(mapi.PrRuleState, int32(r.State))  // PtLong
		bag.Set(mapi.PrRuleName, r.Name)           // PtUnicode
		bag.Set(mapi.PrRuleProvider, r.Provider)   // PtUnicode
		bag.Set(mapi.PrRuleCondition, r.Condition) // PtRestriction
		bag.Set(mapi.PrRuleActions, r.Actions)     // PtActions
		bags = append(bags, bag)
	}
	return bags, nil
}

// RopModifyRules ModifyRulesFlags ([MS-OXORULE] 2.2.1.1): the only valid bit is
// Replace, which clears the folder's whole rule set before applying the batch.
const modifyRulesReplace uint8 = 0x01

// RuleData row flags ([MS-OXORULE] 2.2.1.3.1.1). Dispatch is exact equality, not a
// bitmask test; a flag value outside this set is skipped.
const (
	ruleRowAdd    uint8 = 0x01
	ruleRowModify uint8 = 0x02
	ruleRowRemove uint8 = 0x04
)

// ropModifyRules handles RopModifyRules ([MS-OXORULE] 2.2.1.1): it decodes the
// RuleData rows, each a row flag plus a tagged-property-value array (the same shape
// RopModifyPermissions uses, NOT a PROPERTY_ROW), turns each into a store change, and
// applies the batch. The response is the bare head whose HandleIndex echoes the input
// folder handle.
func (s *Session) ropModifyRules(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	flags, e1 := p.Uint8()  // ModifyRulesFlags
	count, e2 := p.Uint16() // RulesCount
	if e1 != nil || e2 != nil {
		return false
	}
	rows, framed := pullRuleRows(p, int(count))
	if !framed {
		return false
	}
	folder, ok := s.openFolder(out, ropModifyRules, handles, hindex, hindex)
	if !ok {
		return true
	}
	// Editing a folder's rule table requires owner rights.
	if s.denyWrite(out, ropModifyRules, hindex, folder.store, folder.folderID, mapi.FrightsOwner) {
		return true
	}
	changes := ruleChanges(rows)
	if err := folder.store.ModifyRules(folder.folderID, flags&modifyRulesReplace != 0, changes); err != nil {
		writeErr(out, ropModifyRules, hindex, ecError)
		return true
	}

	out.Uint8(ropModifyRules)
	out.Uint8(hindex) // echo the input handle
	out.Uint32(ecSuccess)
	return true
}

// ruleRow is one RuleData row: the operation it asks for and the properties
// carrying the rule's columns.
type ruleRow struct {
	flags    uint8
	propvals mapi.PropertyValues
}

// pullRuleRows reads the request's RuleData array. The rows are parsed before
// the handle is resolved so the batch stays aligned even when the handle turns
// out to be wrong.
func pullRuleRows(p *ext.Pull, count int) ([]ruleRow, bool) {
	rows := make([]ruleRow, 0, count)
	for range count {
		rowFlags, e1 := p.Uint8()
		propvals, e2 := p.PropertyValues()
		if e1 != nil || e2 != nil {
			return nil, false
		}
		rows = append(rows, ruleRow{flags: rowFlags, propvals: propvals})
	}
	return rows, true
}

// ruleChanges turns the request rows into store operations. A modify or remove
// without PR_RULE_ID cannot be keyed and is skipped rather than faulted.
func ruleChanges(rows []ruleRow) []objectstore.RuleChange {
	changes := make([]objectstore.RuleChange, 0, len(rows))
	for _, r := range rows {
		if c, ok := ruleChange(r); ok {
			changes = append(changes, c)
		}
	}
	return changes
}

// ruleChange maps one row to its store operation.
func ruleChange(r ruleRow) (objectstore.RuleChange, bool) {
	switch r.flags {
	case ruleRowAdd:
		return objectstore.RuleChange{Op: objectstore.RuleAdd, Patch: rulePatch(r.propvals)}, true
	case ruleRowModify:
		id, ok := ruleID(r.propvals)
		if !ok {
			return objectstore.RuleChange{}, false
		}
		return objectstore.RuleChange{Op: objectstore.RuleModify, RuleID: id, Patch: rulePatch(r.propvals)}, true
	case ruleRowRemove:
		id, ok := ruleID(r.propvals)
		if !ok {
			return objectstore.RuleChange{}, false
		}
		return objectstore.RuleChange{Op: objectstore.RuleRemove, RuleID: id}, true
	}
	return objectstore.RuleChange{}, false
}

// ruleID reads PR_RULE_ID (PtI8) from a rule row's property bag.
func ruleID(propvals mapi.PropertyValues) (int64, bool) {
	v, ok := propvals.Get(mapi.PrRuleID)
	if !ok {
		return 0, false
	}
	id, ok := v.(int64)
	return id, ok
}

// rulePatch builds the store RulePatch from a RuleData row's property bag: each modeled
// column is set only when the wire row carried it (a nil pointer means absent), so a
// Modify updates exactly what the client sent and an Add fills what it provided.
// PR_RULE_ID and any unmodeled property are ignored here.
func rulePatch(propvals mapi.PropertyValues) objectstore.RulePatch {
	var patch objectstore.RulePatch
	patch.Name = patchValue[string](propvals, mapi.PrRuleName)
	patch.Provider = patchValue[string](propvals, mapi.PrRuleProvider)
	patch.Sequence = patchValue[int32](propvals, mapi.PrRuleSequence)
	patch.Condition = patchValue[mapi.Restriction](propvals, mapi.PrRuleCondition)
	patch.Actions = patchValue[mapi.RuleActions](propvals, mapi.PrRuleActions)
	if n := patchValue[int32](propvals, mapi.PrRuleState); n != nil {
		// #nosec G115 -- the signed and unsigned views of the same 32 bits
		state := uint32(*n)
		patch.State = &state
	}
	return patch
}

// patchValue reads one property as the type the patch field holds, returning nil
// when the row did not carry it or carried it as another type. A nil pointer is
// what tells the store the column is absent, so a Modify leaves it alone.
func patchValue[T any](propvals mapi.PropertyValues, tag mapi.PropTag) *T {
	v, ok := propvals.Get(tag)
	if !ok {
		return nil
	}
	typed, ok := v.(T)
	if !ok {
		return nil
	}
	return &typed
}
