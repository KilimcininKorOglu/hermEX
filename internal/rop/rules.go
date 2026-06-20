package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ropGetRulesTable handles RopGetRulesTable ([MS-OXORULE] 2.2.1.2): it snapshots the
// folder's rules into a new table object whose rows the client reads with
// RopSetColumns/RopQueryRows. The response is the bare 6-byte head — no row count,
// matching the no-extra-body encoding — whose HandleIndex is the OUTPUT handle the
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
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropGetRulesTable, ohindex, ecError)
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
		bag.Set(mapi.PrRuleID, r.ID)               // PtI8
		bag.Set(mapi.PrRuleSequence, r.Sequence)   // PtLong
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
// RuleData rows — each a row flag plus a tagged-property-value array (the same shape
// RopModifyPermissions uses, NOT a PROPERTY_ROW) — turns each into a store change, and
// applies the batch. The response is the bare head whose HandleIndex echoes the input
// folder handle.
func (s *Session) ropModifyRules(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	flags, e1 := p.Uint8()  // ModifyRulesFlags
	count, e2 := p.Uint16() // RulesCount
	if e1 != nil || e2 != nil {
		return false
	}
	type ruleRow struct {
		flags    uint8
		propvals mapi.PropertyValues
	}
	rows := make([]ruleRow, 0, count)
	for i := 0; i < int(count); i++ {
		rowFlags, e3 := p.Uint8()
		propvals, e4 := p.PropertyValues()
		if e3 != nil || e4 != nil {
			return false
		}
		rows = append(rows, ruleRow{flags: rowFlags, propvals: propvals})
	}

	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropModifyRules, hindex, ecError)
		return true
	}
	// Editing a folder's rule table requires owner rights.
	if s.denyWrite(out, ropModifyRules, hindex, folder.store, folder.folderID, mapi.FrightsOwner) {
		return true
	}

	changes := make([]objectstore.RuleChange, 0, len(rows))
	for _, r := range rows {
		switch r.flags {
		case ruleRowAdd:
			changes = append(changes, objectstore.RuleChange{Op: objectstore.RuleAdd, Patch: rulePatch(r.propvals)})
		case ruleRowModify:
			id, ok := ruleID(r.propvals)
			if !ok {
				continue // a modify without PR_RULE_ID cannot be keyed; skip it
			}
			changes = append(changes, objectstore.RuleChange{Op: objectstore.RuleModify, RuleID: id, Patch: rulePatch(r.propvals)})
		case ruleRowRemove:
			id, ok := ruleID(r.propvals)
			if !ok {
				continue
			}
			changes = append(changes, objectstore.RuleChange{Op: objectstore.RuleRemove, RuleID: id})
		}
	}

	if err := folder.store.ModifyRules(folder.folderID, flags&modifyRulesReplace != 0, changes); err != nil {
		writeErr(out, ropModifyRules, hindex, ecError)
		return true
	}

	out.Uint8(ropModifyRules)
	out.Uint8(hindex) // echo the input handle
	out.Uint32(ecSuccess)
	return true
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
	if v, ok := propvals.Get(mapi.PrRuleName); ok {
		if name, ok := v.(string); ok {
			patch.Name = &name
		}
	}
	if v, ok := propvals.Get(mapi.PrRuleProvider); ok {
		if provider, ok := v.(string); ok {
			patch.Provider = &provider
		}
	}
	if v, ok := propvals.Get(mapi.PrRuleSequence); ok {
		if seq, ok := v.(int32); ok {
			patch.Sequence = &seq
		}
	}
	if v, ok := propvals.Get(mapi.PrRuleState); ok {
		if n, ok := v.(int32); ok {
			state := uint32(n)
			patch.State = &state
		}
	}
	if v, ok := propvals.Get(mapi.PrRuleCondition); ok {
		if cond, ok := v.(mapi.Restriction); ok {
			patch.Condition = &cond
		}
	}
	if v, ok := propvals.Get(mapi.PrRuleActions); ok {
		if acts, ok := v.(mapi.RuleActions); ok {
			patch.Actions = &acts
		}
	}
	return patch
}
