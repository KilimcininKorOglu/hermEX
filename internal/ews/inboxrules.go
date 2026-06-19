package ews

import (
	"encoding/xml"
	"net/http"
	"strconv"

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
	Actions        *ruleActions    `xml:"Actions,omitempty"`
}

// rulePredicates carries the curated condition vocabulary, in the
// RulePredicatesType schema field order.
type rulePredicates struct {
	ContainsSenderStrings  []string `xml:"ContainsSenderStrings>String,omitempty"`
	ContainsSubjectStrings []string `xml:"ContainsSubjectStrings>String,omitempty"`
	Importance             string   `xml:"Importance,omitempty"`
}

// ruleActions carries the curated action vocabulary, in the RuleActionsType schema
// field order.
type ruleActions struct {
	Delete       bool          `xml:"Delete,omitempty"`
	MarkAsRead   bool          `xml:"MarkAsRead,omitempty"`
	MoveToFolder *targetFolder `xml:"MoveToFolder,omitempty"`
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
