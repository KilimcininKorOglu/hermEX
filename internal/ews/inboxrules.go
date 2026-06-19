package ews

import (
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// Inbox rules (MS-OXWSMSG GetInboxRules / UpdateInboxRules) over hermEX's stored
// rule set. The wire Rule format is rich (dozens of predicates and actions); v1
// maps the curated vocabulary hermEX can actually evaluate and run on delivery:
//   conditions — ContainsSubjectStrings, ContainsSenderStrings, Importance
//   actions    — MoveToFolder, Delete, MarkAsRead
// Each maps to exactly one stored RESTRICTION / RULE_ACTIONS shape, so a rule
// round-trips read→edit→write without drifting. A stored rule using anything
// outside this set is still listed (RuleId/DisplayName/Priority/IsEnabled) but
// marked IsNotSupported so a client shows it un-editably rather than dropping it.

// --- wire types ---

type getInboxRulesRequest struct {
	MailboxSmtpAddress string `xml:"MailboxSmtpAddress"`
}

// getInboxRulesResponse is a single response message (GetInboxRulesResponseType
// extends ResponseMessageType directly — there is no ResponseMessages wrapper).
type getInboxRulesResponse struct {
	XMLName               xml.Name    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetInboxRulesResponse"`
	ResponseClass         string      `xml:"ResponseClass,attr"`
	ResponseCode          string      `xml:"ResponseCode"`
	OutlookRuleBlobExists bool        `xml:"OutlookRuleBlobExists"`
	InboxRules            *inboxRules `xml:"InboxRules,omitempty"`
}

// inboxRules switches the rule list to the types namespace (InboxRules is a
// messages-namespace element whose Rule children are types-namespace).
type inboxRules struct {
	Rules []ewsRule `xml:"http://schemas.microsoft.com/exchange/services/2006/types Rule"`
}

type ewsRule struct {
	RuleID         string          `xml:"RuleId,omitempty"`
	DisplayName    string          `xml:"DisplayName"`
	Priority       int             `xml:"Priority"`
	IsEnabled      bool            `xml:"IsEnabled"`
	IsNotSupported bool            `xml:"IsNotSupported,omitempty"`
	Conditions     *rulePredicates `xml:"Conditions,omitempty"`
	Exceptions     *rulePredicates `xml:"Exceptions,omitempty"`
	Actions        *ruleActions    `xml:"Actions,omitempty"`
}

// rulePredicates carries the curated condition vocabulary, in the
// RulePredicatesType schema field order. Unknown collects any predicate element
// outside that vocabulary (xml:",any"), so the write side can reject a rule it
// cannot faithfully store rather than silently dropping the condition.
type rulePredicates struct {
	ContainsSenderStrings  []string  `xml:"ContainsSenderStrings>String,omitempty"`
	ContainsSubjectStrings []string  `xml:"ContainsSubjectStrings>String,omitempty"`
	Importance             string    `xml:"Importance,omitempty"`
	Unknown                []anyElem `xml:",any"`
}

// ruleActions carries the curated action vocabulary, in the RuleActionsType schema
// field order. Unknown collects any action outside that vocabulary, so the write
// side rejects rather than silently dropping it.
type ruleActions struct {
	Delete       bool          `xml:"Delete,omitempty"`
	MarkAsRead   bool          `xml:"MarkAsRead,omitempty"`
	MoveToFolder *targetFolder `xml:"MoveToFolder,omitempty"`
	Unknown      []anyElem     `xml:",any"`
}

// anyElem captures an unrecognized child element's name for the ",any" catch-all.
type anyElem struct {
	XMLName xml.Name
}

// targetFolder is the TargetFolderIdType FolderId hermEX emits/accepts for a move.
type targetFolder struct {
	FolderID *oxews.FolderID `xml:"FolderId"`
	// A move target may also arrive as a distinguished folder id on write.
	Distinguished *refID `xml:"DistinguishedFolderId"`
}

// --- handler ---

// handleGetInboxRules answers GetInboxRules: it lists the requester's inbox rules,
// mapping each stored rule to a wire Rule. OutlookRuleBlobExists is always false —
// hermEX keeps no Outlook rule blob; the rules are the authoritative set.
func (s *Server) handleGetInboxRules(w http.ResponseWriter, inner []byte, sess *session) {
	var req getInboxRulesRequest
	_ = xml.Unmarshal(inner, &req) // MailboxSmtpAddress is advisory; v1 serves the requester's own inbox

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	rules, err := st.ListRules(int64(mapi.PrivateFIDInbox))
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}

	resp := getInboxRulesResponse{ResponseClass: "Success", ResponseCode: "NoError"}
	if len(rules) > 0 {
		list := &inboxRules{Rules: make([]ewsRule, 0, len(rules))}
		for _, r := range rules {
			list.Rules = append(list.Rules, ruleToWire(r))
		}
		resp.InboxRules = list
	}
	writeResponse(w, resp)
}

// ruleToWire maps a stored rule to a wire Rule. A rule whose condition or actions
// fall outside the curated vocabulary is marked IsNotSupported and carries no
// Conditions/Actions (so a client lists it without misrepresenting it).
func ruleToWire(r objectstore.Rule) ewsRule {
	out := ewsRule{
		RuleID:      strconv.FormatInt(r.ID, 10),
		DisplayName: r.Name,
		Priority:    int(r.Sequence),
		IsEnabled:   r.Enabled(),
	}
	conds, condOK := predicatesFromRestriction(r.Condition)
	acts, actOK := actionsFromBlocks(r.Actions)
	if condOK && actOK {
		if !conds.empty() {
			out.Conditions = &conds
		}
		out.Actions = &acts
	} else {
		out.IsNotSupported = true
	}
	return out
}

// empty reports whether no curated predicate is set (a rule that matches every
// message), so Conditions can be omitted.
func (p rulePredicates) empty() bool {
	return len(p.ContainsSenderStrings) == 0 && len(p.ContainsSubjectStrings) == 0 && p.Importance == ""
}

// predicatesFromRestriction maps a stored RESTRICTION to the curated predicates,
// reporting ok=false when any part falls outside the recognized vocabulary. An
// empty restriction (a rule that matches everything) is recognized as no predicates.
func predicatesFromRestriction(r mapi.Restriction) (rulePredicates, bool) {
	var p rulePredicates
	// ResNull is the explicit "no restriction" type. ResAnd is 0x00, so it must NOT
	// be treated as the zero value here — an empty AND is handled in mergePredicate.
	if r.Type == mapi.ResNull {
		return p, true
	}
	ok := mergePredicate(&p, r)
	return p, ok
}

// mergePredicate folds one restriction node into p: ResAnd recurses, ResOr of
// same-tag content becomes an array predicate, and a leaf maps to its field.
func mergePredicate(p *rulePredicates, r mapi.Restriction) bool {
	switch r.Type {
	case mapi.ResAnd:
		if r.Value == nil {
			return true // an empty AND matches every message — no predicates
		}
		kids, ok := r.Value.([]mapi.Restriction)
		if !ok {
			return false
		}
		for _, k := range kids {
			if !mergePredicate(p, k) {
				return false
			}
		}
		return true
	case mapi.ResOr:
		kids, ok := r.Value.([]mapi.Restriction)
		if !ok {
			return false
		}
		return mergeContentOr(p, kids)
	case mapi.ResContent:
		c, ok := r.Value.(mapi.ContentRestriction)
		if !ok {
			return false
		}
		return appendContains(p, c.PropTag, contentString(c))
	case mapi.ResProperty:
		pr, ok := r.Value.(mapi.PropertyRestriction)
		if !ok || pr.PropTag != mapi.PrImportance || pr.Relop != mapi.RelopEQ {
			return false
		}
		n, _ := pr.PropVal.Value.(int32)
		p.Importance = importanceWire(int(n))
		return p.Importance != ""
	}
	return false
}

// mergeContentOr folds an OR of same-tag content restrictions into the matching
// array predicate (the form a multi-string ContainsXStrings predicate is stored as).
func mergeContentOr(p *rulePredicates, kids []mapi.Restriction) bool {
	if len(kids) == 0 {
		return false
	}
	var tag mapi.PropTag
	vals := make([]string, 0, len(kids))
	for i, k := range kids {
		if k.Type != mapi.ResContent {
			return false
		}
		c, ok := k.Value.(mapi.ContentRestriction)
		if !ok {
			return false
		}
		if i == 0 {
			tag = c.PropTag
		} else if c.PropTag != tag {
			return false
		}
		vals = append(vals, contentString(c))
	}
	for _, v := range vals {
		if !appendContains(p, tag, v) {
			return false
		}
	}
	return true
}

// appendContains routes a substring match on a recognized tag to its predicate.
func appendContains(p *rulePredicates, tag mapi.PropTag, val string) bool {
	switch tag {
	case mapi.PrSubject:
		p.ContainsSubjectStrings = append(p.ContainsSubjectStrings, val)
		return true
	case mapi.PrSenderSmtpAddress:
		p.ContainsSenderStrings = append(p.ContainsSenderStrings, val)
		return true
	}
	return false
}

// contentString reads the string value of a content restriction.
func contentString(c mapi.ContentRestriction) string {
	s, _ := c.PropVal.Value.(string)
	return s
}

// actionsFromBlocks maps stored action blocks to the curated wire actions,
// reporting ok=false on any block outside the recognized set.
func actionsFromBlocks(a mapi.RuleActions) (ruleActions, bool) {
	var out ruleActions
	for _, b := range a.Blocks {
		switch b.Type {
		case mapi.OpMarkAsRead:
			out.MarkAsRead = true
		case mapi.OpDelete:
			out.Delete = true
		case mapi.OpMove:
			mc, ok := b.Data.(mapi.MoveCopyAction)
			if !ok {
				return ruleActions{}, false
			}
			svr, ok := mc.FolderEID.(mapi.SVREID)
			if !ok {
				return ruleActions{}, false
			}
			fid := oxews.EncodeFolderID(int64(svr.FolderID))
			out.MoveToFolder = &targetFolder{FolderID: &oxews.FolderID{ID: fid}}
		default:
			return ruleActions{}, false
		}
	}
	return out, true
}

// importanceWire maps a PR_IMPORTANCE level to the EWS ImportanceChoicesType value,
// or "" for an unknown level.
func importanceWire(level int) string {
	switch level {
	case mapi.ImportanceLow:
		return "Low"
	case mapi.ImportanceNormal:
		return "Normal"
	case mapi.ImportanceHigh:
		return "High"
	}
	return ""
}

// --- UpdateInboxRules (write) ---

const (
	opCreate = "Create"
	opSet    = "Set"
	opDelete = "Delete"
)

type updateInboxRulesRequest struct {
	MailboxSmtpAddress    string         `xml:"MailboxSmtpAddress"`
	RemoveOutlookRuleBlob bool           `xml:"RemoveOutlookRuleBlob"`
	Operations            ruleOperations `xml:"Operations"`
}

// ruleOperations is the ordered Create/Set/Delete operation list. A custom
// unmarshaler preserves document order so a RuleOperationError's OperationIndex
// matches the request — a per-element-type split would lose the interleaving.
type ruleOperations struct {
	Ops []ruleOperation
}

type ruleOperation struct {
	Kind   string
	Rule   ewsRule // Create / Set
	RuleID string  // Delete
}

// UnmarshalXML reads the operation children in document order.
func (o *ruleOperations) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "CreateRuleOperation":
				var op struct {
					Rule ewsRule `xml:"Rule"`
				}
				if err := d.DecodeElement(&op, &t); err != nil {
					return err
				}
				o.Ops = append(o.Ops, ruleOperation{Kind: opCreate, Rule: op.Rule})
			case "SetRuleOperation":
				var op struct {
					Rule ewsRule `xml:"Rule"`
				}
				if err := d.DecodeElement(&op, &t); err != nil {
					return err
				}
				o.Ops = append(o.Ops, ruleOperation{Kind: opSet, Rule: op.Rule})
			case "DeleteRuleOperation":
				var op struct {
					RuleID string `xml:"RuleId"`
				}
				if err := d.DecodeElement(&op, &t); err != nil {
					return err
				}
				o.Ops = append(o.Ops, ruleOperation{Kind: opDelete, RuleID: op.RuleID})
			default:
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}

// updateInboxRulesResponse is a single response message (UpdateInboxRulesResponseType
// extends ResponseMessageType directly).
type updateInboxRulesResponse struct {
	XMLName             xml.Name      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateInboxRulesResponse"`
	ResponseClass       string        `xml:"ResponseClass,attr"`
	ResponseCode        string        `xml:"ResponseCode"`
	RuleOperationErrors *ruleOpErrors `xml:"RuleOperationErrors,omitempty"`
}

type ruleOpErrors struct {
	Errors []ruleOperationError `xml:"http://schemas.microsoft.com/exchange/services/2006/types RuleOperationError"`
}

type ruleOperationError struct {
	OperationIndex   int                  `xml:"OperationIndex"`
	ValidationErrors ruleValidationErrors `xml:"ValidationErrors"`
}

type ruleValidationErrors struct {
	Errors []ruleValidationError `xml:"Error"`
}

type ruleValidationError struct {
	FieldURI     string `xml:"FieldURI"`
	ErrorCode    string `xml:"ErrorCode"`
	ErrorMessage string `xml:"ErrorMessage"`
	FieldValue   string `xml:"FieldValue"`
}

// ruleOpError builds a single-error RuleOperationError for the operation at index.
func ruleOpError(index int, fieldURI, code, message string) ruleOperationError {
	return ruleOperationError{
		OperationIndex: index,
		ValidationErrors: ruleValidationErrors{Errors: []ruleValidationError{{
			FieldURI: fieldURI, ErrorCode: code, ErrorMessage: message,
		}}},
	}
}

// handleUpdateInboxRules answers UpdateInboxRules: it validates every operation,
// and — matching MS-OXWSMSG's atomic semantics — applies the whole batch only if
// all operations are valid, else applies nothing and returns the per-operation
// RuleOperationErrors. RemoveOutlookRuleBlob is ignored (hermEX keeps no blob).
func (s *Server) handleUpdateInboxRules(w http.ResponseWriter, inner []byte, sess *session) {
	var req updateInboxRulesRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "UpdateInboxRules: "+err.Error())
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	changes := make([]objectstore.RuleChange, 0, len(req.Operations.Ops))
	var opErrors []ruleOperationError
	for i, op := range req.Operations.Ops {
		change, opErr, ok := ruleChangeFromOp(op, i)
		if !ok {
			opErrors = append(opErrors, opErr)
			continue
		}
		changes = append(changes, change)
	}

	if len(opErrors) > 0 {
		writeResponse(w, updateInboxRulesResponse{
			ResponseClass:       "Error",
			ResponseCode:        "ErrorInboxRulesValidationError",
			RuleOperationErrors: &ruleOpErrors{Errors: opErrors},
		})
		return
	}
	if len(changes) > 0 {
		if err := st.ModifyRules(int64(mapi.PrivateFIDInbox), false, changes); err != nil {
			writeSOAPFault(w, "ErrorInternalServerError", err.Error())
			return
		}
	}
	writeResponse(w, updateInboxRulesResponse{ResponseClass: "Success", ResponseCode: "NoError"})
}

// ruleChangeFromOp maps one wire operation to a store RuleChange, or a
// RuleOperationError when the rule falls outside the curated vocabulary.
func ruleChangeFromOp(op ruleOperation, index int) (objectstore.RuleChange, ruleOperationError, bool) {
	switch op.Kind {
	case opDelete:
		id, err := strconv.ParseInt(op.RuleID, 10, 64)
		if err != nil {
			return objectstore.RuleChange{}, ruleOpError(index, "RuleId", "RuleNotFound", "rule id is not a known rule"), false
		}
		return objectstore.RuleChange{Op: objectstore.RuleRemove, RuleID: id}, ruleOperationError{}, true
	case opCreate, opSet:
		patch, opErr, ok := rulePatchFromWire(op.Rule, index)
		if !ok {
			return objectstore.RuleChange{}, opErr, false
		}
		if op.Kind == opSet {
			id, err := strconv.ParseInt(op.Rule.RuleID, 10, 64)
			if err != nil {
				return objectstore.RuleChange{}, ruleOpError(index, "RuleId", "RuleNotFound", "rule id is not a known rule"), false
			}
			return objectstore.RuleChange{Op: objectstore.RuleModify, RuleID: id, Patch: patch}, ruleOperationError{}, true
		}
		return objectstore.RuleChange{Op: objectstore.RuleAdd, Patch: patch}, ruleOperationError{}, true
	}
	return objectstore.RuleChange{}, ruleOpError(index, "IsNotSupported", "UnsupportedRule", "unsupported operation"), false
}

// rulePatchFromWire maps a wire Rule to a store RulePatch, rejecting a rule whose
// condition, exception, or action hermEX cannot faithfully store.
func rulePatchFromWire(r ewsRule, index int) (objectstore.RulePatch, ruleOperationError, bool) {
	if r.Exceptions != nil {
		return objectstore.RulePatch{}, ruleOpError(index, "IsNotSupported", "UnsupportedRule", "rule exceptions are not supported"), false
	}
	cond := matchAllRestriction()
	if r.Conditions != nil {
		c, ok := restrictionFromPredicates(*r.Conditions)
		if !ok {
			return objectstore.RulePatch{}, ruleOpError(index, "IsNotSupported", "UnsupportedRule", "rule condition is not supported"), false
		}
		cond = c
	}
	if r.Actions == nil {
		return objectstore.RulePatch{}, ruleOpError(index, "Actions", "MissingAction", "a rule must have at least one action"), false
	}
	acts, ok := blocksFromActions(*r.Actions)
	if !ok {
		return objectstore.RulePatch{}, ruleOpError(index, "Actions", "UnsupportedRule", "rule action is not supported"), false
	}
	name := r.DisplayName
	seq := int32(r.Priority)
	state := uint32(0)
	if r.IsEnabled {
		state = mapi.RuleStateEnabled
	}
	return objectstore.RulePatch{
		Name: &name, Sequence: &seq, State: &state, Condition: &cond, Actions: &acts,
	}, ruleOperationError{}, true
}

// restrictionFromPredicates builds the stored RESTRICTION from the curated
// predicates: a single leaf, an AND of leaves, or (for a multi-string array) an OR.
// It reports ok=false when an unrecognized predicate is present.
func restrictionFromPredicates(p rulePredicates) (mapi.Restriction, bool) {
	if len(p.Unknown) > 0 {
		return mapi.Restriction{}, false
	}
	var leaves []mapi.Restriction
	if len(p.ContainsSubjectStrings) > 0 {
		leaves = append(leaves, containsLeaf(objectstore.RuleSubjectContains, p.ContainsSubjectStrings))
	}
	if len(p.ContainsSenderStrings) > 0 {
		leaves = append(leaves, containsLeaf(objectstore.RuleFromContains, p.ContainsSenderStrings))
	}
	if p.Importance != "" {
		level, ok := importanceLevel(p.Importance)
		if !ok {
			return mapi.Restriction{}, false
		}
		leaves = append(leaves, objectstore.RuleImportanceIs(level))
	}
	switch len(leaves) {
	case 0:
		return matchAllRestriction(), true
	case 1:
		return leaves[0], true
	default:
		return mapi.Restriction{Type: mapi.ResAnd, Value: leaves}, true
	}
}

// containsLeaf builds a single content restriction or, for multiple strings, an OR
// of them — the same form the read side recognizes as an array predicate.
func containsLeaf(build func(string) mapi.Restriction, vals []string) mapi.Restriction {
	if len(vals) == 1 {
		return build(vals[0])
	}
	kids := make([]mapi.Restriction, len(vals))
	for i, v := range vals {
		kids[i] = build(v)
	}
	return mapi.Restriction{Type: mapi.ResOr, Value: kids}
}

// blocksFromActions builds the stored action blocks from the curated wire actions,
// reporting ok=false on an unrecognized action, a bad move target, or no action.
func blocksFromActions(a ruleActions) (mapi.RuleActions, bool) {
	if len(a.Unknown) > 0 {
		return mapi.RuleActions{}, false
	}
	var blocks []mapi.ActionBlock
	if a.MarkAsRead {
		blocks = append(blocks, objectstore.RuleMarkReadAction())
	}
	if a.Delete {
		blocks = append(blocks, objectstore.RuleDeleteAction())
	}
	if a.MoveToFolder != nil {
		fid, ok := resolveMoveTarget(*a.MoveToFolder)
		if !ok {
			return mapi.RuleActions{}, false
		}
		blocks = append(blocks, objectstore.RuleMoveAction(fid))
	}
	if len(blocks) == 0 {
		return mapi.RuleActions{}, false
	}
	return mapi.RuleActions{Blocks: blocks}, true
}

// resolveMoveTarget resolves a move action's destination to a folder id, from an
// opaque FolderId or a distinguished folder name.
func resolveMoveTarget(t targetFolder) (int64, bool) {
	if t.FolderID != nil && t.FolderID.ID != "" {
		fid, err := oxews.DecodeFolderID(t.FolderID.ID)
		return fid, err == nil
	}
	if t.Distinguished != nil {
		fid, ok := distinguishedFolders[strings.ToLower(t.Distinguished.ID)]
		return fid, ok
	}
	return 0, false
}

// importanceLevel maps an EWS ImportanceChoicesType value to a PR_IMPORTANCE level.
func importanceLevel(v string) (int, bool) {
	switch v {
	case "Low":
		return mapi.ImportanceLow, true
	case "Normal":
		return mapi.ImportanceNormal, true
	case "High":
		return mapi.ImportanceHigh, true
	}
	return 0, false
}

// matchAllRestriction is the empty AND a no-condition rule stores (it matches every
// message).
func matchAllRestriction() mapi.Restriction {
	return mapi.Restriction{Type: mapi.ResAnd, Value: []mapi.Restriction{}}
}
