package objectstore

import (
	"strings"

	"hermex/internal/mapi"
)

// Fuzzy-level bits for a ContentRestriction (MS-OXCDATA §2.12.3.1). The low word
// selects the match kind; the high word carries case/diacritic flags.
const (
	flFullString = 0x0000 // match the whole value
	flSubstring  = 0x0001 // match a substring
	flPrefix     = 0x0002 // match a leading prefix
	flIgnoreCase = 0x00010000
)

// ruleFuzzyContains is the fuzzy level the curated "contains" conditions use:
// a case-insensitive substring match.
const ruleFuzzyContains = flSubstring | flIgnoreCase

// evalRestriction reports whether a restriction matches a message's property
// bag. It is a pure recursive walk over the restriction tree; an absent property
// fails a leaf rather than erroring, so a rule simply does not match a message
// that lacks the tested property. Node kinds the rule editor does not produce
// (recipient sub-objects, property-to-property compares, counts) evaluate to
// false — they are never silently treated as a match.
func evalRestriction(r mapi.Restriction, props mapi.PropertyValues) bool {
	switch r.Type {
	case mapi.ResAnd:
		kids, _ := r.Value.([]mapi.Restriction)
		for _, k := range kids {
			if !evalRestriction(k, props) {
				return false
			}
		}
		return true
	case mapi.ResOr:
		kids, _ := r.Value.([]mapi.Restriction)
		for _, k := range kids {
			if evalRestriction(k, props) {
				return true
			}
		}
		return false
	case mapi.ResNot:
		inner, ok := r.Value.(mapi.Restriction)
		if !ok {
			return false
		}
		return !evalRestriction(inner, props)
	case mapi.ResContent:
		c, ok := r.Value.(mapi.ContentRestriction)
		return ok && evalContent(c, props)
	case mapi.ResProperty:
		pr, ok := r.Value.(mapi.PropertyRestriction)
		return ok && evalProperty(pr, props)
	case mapi.ResBitmask:
		b, ok := r.Value.(mapi.BitmaskRestriction)
		return ok && evalBitmask(b, props)
	case mapi.ResSize:
		sz, ok := r.Value.(mapi.SizeRestriction)
		return ok && evalSize(sz, props)
	case mapi.ResExist:
		e, ok := r.Value.(mapi.ExistRestriction)
		return ok && props.Has(e.PropTag)
	case mapi.ResComment:
		// A comment annotates an optional child restriction; the annotation
		// itself imposes no constraint, so an absent child matches.
		c, ok := r.Value.(mapi.CommentRestriction)
		if !ok {
			return false
		}
		if c.Res == nil {
			return true
		}
		return evalRestriction(*c.Res, props)
	case mapi.ResNull:
		// A null restriction is the absence of a constraint: it matches every
		// message (MS-OXCDATA). The rule editor never emits this.
		return true
	default:
		// ResSub, ResPropCompare, ResCount, ResAnnotation, unknown: unsupported.
		return false
	}
}

// evalContent matches a string property against the search value with the
// fuzzy level's match kind and case sensitivity. Only string-valued properties
// participate; a non-string property or value fails the match.
func evalContent(c mapi.ContentRestriction, props mapi.PropertyValues) bool {
	v, ok := props.Get(c.PropTag)
	if !ok {
		return false
	}
	hay, ok := v.(string)
	if !ok {
		return false
	}
	needle, ok := c.PropVal.Value.(string)
	if !ok {
		return false
	}
	if c.FuzzyLevel&flIgnoreCase != 0 {
		hay = strings.ToLower(hay)
		needle = strings.ToLower(needle)
	}
	switch c.FuzzyLevel & 0x0000FFFF {
	case flFullString:
		return hay == needle
	case flPrefix:
		return strings.HasPrefix(hay, needle)
	default: // flSubstring and any unrecognized low word
		return strings.Contains(hay, needle)
	}
}

// evalProperty compares a property against a value with a relational operator.
// Integer-typed values (importance, message size, delivery time) compare
// numerically; string values compare lexically. A type mismatch fails.
func evalProperty(pr mapi.PropertyRestriction, props mapi.PropertyValues) bool {
	have, ok := props.Get(pr.PropTag)
	if !ok {
		return false
	}
	if hn, hok := toInt64(have); hok {
		if wn, wok := toInt64(pr.PropVal.Value); wok {
			return applyRelop(cmpInt64(hn, wn), pr.Relop)
		}
		return false
	}
	if hs, hok := have.(string); hok {
		if ws, wok := pr.PropVal.Value.(string); wok {
			return applyRelop(strings.Compare(hs, ws), pr.Relop)
		}
	}
	return false
}

// evalBitmask tests masked bits of an integer property.
func evalBitmask(b mapi.BitmaskRestriction, props mapi.PropertyValues) bool {
	v, ok := props.Get(b.PropTag)
	if !ok {
		return false
	}
	n, ok := toInt64(v)
	if !ok {
		return false
	}
	masked := uint32(n) & b.Mask
	if b.Relop == mapi.BmrEqz {
		return masked == 0
	}
	return masked != 0
}

// evalSize compares the byte size of a property's value against a threshold.
func evalSize(sz mapi.SizeRestriction, props mapi.PropertyValues) bool {
	v, ok := props.Get(sz.PropTag)
	if !ok {
		return false
	}
	return applyRelop(cmpInt64(int64(valueByteSize(v)), int64(sz.Size)), sz.Relop)
}

// toInt64 coerces an integer-typed property value to int64, reporting ok=false
// for non-integer values. It covers the MAPI integer types a rule condition
// compares (PtLong int32, PtI8/PtCurrency int64, PtSysTime uint64).
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case int16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

// valueByteSize returns the byte length of a property value for a size
// restriction: the length of a string or binary, or the fixed width of a scalar.
func valueByteSize(v any) int {
	switch x := v.(type) {
	case string:
		return len(x)
	case []byte:
		return len(x)
	case int16:
		return 2
	case int32, uint32:
		return 4
	case int64, uint64:
		return 8
	default:
		return 0
	}
}

// cmpInt64 returns -1, 0, or 1 as a is less than, equal to, or greater than b.
func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// applyRelop maps a comparison sign (-1/0/1) through a relational operator. The
// regex operator is not supported here and never matches.
func applyRelop(cmp int, op mapi.Relop) bool {
	switch op {
	case mapi.RelopLT:
		return cmp < 0
	case mapi.RelopLE:
		return cmp <= 0
	case mapi.RelopGT:
		return cmp > 0
	case mapi.RelopGE:
		return cmp >= 0
	case mapi.RelopEQ:
		return cmp == 0
	case mapi.RelopNE:
		return cmp != 0
	default:
		return false
	}
}

// RuleRunResult summarizes an on-demand rule run over a folder.
type RuleRunResult struct {
	Evaluated int // messages examined
	Affected  int // messages a matching rule acted on (moved, deleted, or marked read)
}

// RunRules evaluates a folder's enabled rules against every message in it, in
// PR_RULE_SEQUENCE order, applying the actions of each matching rule. It is the
// on-demand entry point (a user's "apply rules now"); incoming mail is processed
// per-message as it is delivered. A move or delete is terminal for that message:
// once it leaves the folder, no further rule is evaluated against it.
func (s *Store) RunRules(folderID int64) (RuleRunResult, error) {
	var res RuleRunResult
	rules, err := s.ListRules(folderID)
	if err != nil {
		return res, err
	}
	if len(rules) == 0 {
		return res, nil
	}
	msgs, err := s.ListMessages(folderID)
	if err != nil {
		return res, err
	}
	for _, m := range msgs {
		res.Evaluated++
		// "Apply rules now" deliberately discards forward requests: re-running rules
		// over an existing folder must not re-send (mass-forward) old mail. Forwards
		// fire only at delivery, through ApplyInboxRules.
		acted, _, err := s.applyRulesToMessage(folderID, m, rules)
		if err != nil {
			return res, err
		}
		if acted {
			res.Affected++
		}
	}
	return res, nil
}

// ApplyInboxRules applies the inbox's enabled rules to a single just-delivered
// message, in sequence order, stopping at a terminal move or delete. It is the
// delivery-time entry point. The message is already filed in the inbox before
// this runs, so a caller on the delivery path must treat a returned error as
// advisory (log and continue) rather than failing delivery — a rule must never
// be able to make a sender retry. A mailbox with no inbox rules is a no-op. Any
// forward actions are returned for the delivery path to enqueue (the store cannot
// send mail); the caller applies the loop/abuse guards before sending.
func (s *Store) ApplyInboxRules(m MessageInfo) ([]ForwardRequest, error) {
	inbox := int64(mapi.PrivateFIDInbox)
	rules, err := s.ListRules(inbox)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, nil
	}
	_, forwards, err := s.applyRulesToMessage(inbox, m, rules)
	return forwards, err
}

// applyRulesToMessage runs a folder's pre-loaded rules against one message,
// applying matching rules' actions in sequence order until a terminal action
// (move/delete) fires or a matching rule carries the ST_EXIT_LEVEL flag. The
// message's byte size is injected as PR_MESSAGE_SIZE so size conditions work
// against a value the property bag does not itself store. It reports whether any
// rule acted on the message.
func (s *Store) applyRulesToMessage(folderID int64, m MessageInfo, rules []Rule) (bool, []ForwardRequest, error) {
	props, err := s.GetMessageProperties(m.ID)
	if err != nil {
		return false, nil, err
	}
	props.Set(mapi.PrMessageSize, int32(m.Size))

	acted := false
	var forwards []ForwardRequest
	for _, r := range rules {
		if !r.Enabled() {
			continue
		}
		if !evalRestriction(r.Condition, props) {
			continue
		}
		terminal, fwds, err := s.applyRuleActions(folderID, m.UID, r.Actions)
		forwards = append(forwards, fwds...)
		if err != nil {
			return acted, forwards, err
		}
		acted = true
		if terminal || r.State&mapi.RuleStateExitLevel != 0 {
			return acted, forwards, nil
		}
	}
	return acted, forwards, nil
}

// applyRuleActions applies a matched rule's action blocks to a message in
// srcFolder. It returns terminal=true once an action moves or deletes the
// message, since its uid in srcFolder is then no longer valid and no further
// rule may run against it. Unsupported action types are skipped.
func (s *Store) applyRuleActions(srcFolder int64, uid uint32, acts mapi.RuleActions) (bool, []ForwardRequest, error) {
	var forwards []ForwardRequest
	for _, b := range acts.Blocks {
		switch b.Type {
		case mapi.OpMarkAsRead:
			cur, err := s.MessageFlags(srcFolder, uid)
			if err != nil {
				return false, forwards, err
			}
			if cur&FlagSeen == 0 {
				if err := s.SetMessageFlags(srcFolder, uid, cur|FlagSeen); err != nil {
					return false, forwards, err
				}
			}
		case mapi.OpTag:
			// Set the property the action carries (the editor uses it to categorize:
			// the named Keywords property the categories UI reads). Non-terminal.
			tv, ok := b.Data.(mapi.TaggedPropVal)
			if !ok {
				continue
			}
			mi, err := s.MessageByUID(srcFolder, uid)
			if err != nil {
				return false, forwards, err
			}
			var pv mapi.PropertyValues
			pv.Set(tv.Tag, tv.Value)
			if err := s.SetMessageProperties(mi.ID, pv); err != nil {
				return false, forwards, err
			}
		case mapi.OpForward:
			// Forward is non-terminal: the message stays in srcFolder. The send
			// itself is the delivery path's job (the store cannot send mail); it
			// sends the original received bytes, so the store collects only the
			// addresses here.
			if addrs := forwardAddresses(b.Data); len(addrs) > 0 {
				forwards = append(forwards, ForwardRequest{To: addrs})
			}
		case mapi.OpCopy:
			dst, ok := moveTargetFolder(b.Data)
			if !ok {
				continue
			}
			if err := s.copyMessage(srcFolder, uid, dst); err != nil {
				return false, forwards, err
			}
			// Copy is non-terminal: the message stays in srcFolder, so later
			// blocks and rules still apply to the original.
		case mapi.OpMove:
			dst, ok := moveTargetFolder(b.Data)
			if !ok {
				continue
			}
			if err := s.moveMessage(srcFolder, uid, dst); err != nil {
				return false, forwards, err
			}
			return true, forwards, nil
		case mapi.OpDelete:
			trash := int64(mapi.PrivateFIDDeletedItems)
			if srcFolder == trash {
				if err := s.DeleteMessage(srcFolder, uid); err != nil {
					return false, forwards, err
				}
			} else if err := s.moveMessage(srcFolder, uid, trash); err != nil {
				return false, forwards, err
			}
			return true, forwards, nil
		}
	}
	return false, forwards, nil
}

// moveTargetFolder extracts the destination folder id from a same-store
// MoveCopyAction, whose FolderEID carries an SVREID holding the folder id.
func moveTargetFolder(data any) (int64, bool) {
	mc, ok := data.(mapi.MoveCopyAction)
	if !ok {
		return 0, false
	}
	svr, ok := mc.FolderEID.(mapi.SVREID)
	if !ok {
		return 0, false
	}
	return int64(svr.FolderID), true
}

// moveMessage re-files a message into dst, preserving its flags and internal
// date, then removes it from src. This mirrors the copy-then-delete move the
// webmail action path performs.
func (s *Store) moveMessage(src int64, uid uint32, dst int64) error {
	_, err := s.MoveMessage(src, uid, dst)
	return err
}

// copyMessage duplicates a message into dst (a fresh uid, the wire form
// re-synthesized), preserving flags and internal date and leaving the source in
// place. It is the non-terminal counterpart of moveMessage, used by an OpCopy rule
// action.
func (s *Store) copyMessage(src int64, uid uint32, dst int64) error {
	m, err := s.MessageByUID(src, uid)
	if err != nil {
		return err
	}
	raw, err := s.GetMessageRaw(src, uid)
	if err != nil {
		return err
	}
	_, err = s.AppendMessage(dst, raw, m.InternalDate, m.Flags)
	return err
}

// MoveMessage moves a message from the src folder to dst by copying its raw
// content, date, and flags into dst and deleting the source. It returns the
// destination copy's info, whose UID is the new per-folder server id.
func (s *Store) MoveMessage(src int64, uid uint32, dst int64) (MessageInfo, error) {
	m, err := s.MessageByUID(src, uid)
	if err != nil {
		return MessageInfo{}, err
	}
	raw, err := s.GetMessageRaw(src, uid)
	if err != nil {
		return MessageInfo{}, err
	}
	info, err := s.AppendMessage(dst, raw, m.InternalDate, m.Flags)
	if err != nil {
		return MessageInfo{}, err
	}
	if err := s.DeleteMessage(src, uid); err != nil {
		return MessageInfo{}, err
	}
	return info, nil
}

// The Rule* constructors below build the curated condition and action vocabulary
// the rule editor exposes. Centralizing them keeps the RESTRICTION fuzzy levels
// and the move action's SVREID encoding in one place, shared by the editor and
// the tests.

// RuleSubjectContains matches messages whose subject contains text
// (case-insensitive).
func RuleSubjectContains(text string) mapi.Restriction {
	return contentContains(mapi.PrSubject, text)
}

// RuleFromContains matches messages whose sender SMTP address contains text
// (case-insensitive).
func RuleFromContains(text string) mapi.Restriction {
	return contentContains(mapi.PrSenderSmtpAddress, text)
}

// RuleBodyContains matches messages whose body text contains text
// (case-insensitive). PR_BODY is set by the MIME import, so the condition matches
// against a property the delivered message actually carries.
func RuleBodyContains(text string) mapi.Restriction {
	return contentContains(mapi.PrBody, text)
}

// RuleAll combines sub-conditions so the rule matches only when EVERY one matches
// (a ResAnd node).
func RuleAll(conds ...mapi.Restriction) mapi.Restriction {
	return mapi.Restriction{Type: mapi.ResAnd, Value: append([]mapi.Restriction(nil), conds...)}
}

// RuleAny combines sub-conditions so the rule matches when ANY one matches (a
// ResOr node).
func RuleAny(conds ...mapi.Restriction) mapi.Restriction {
	return mapi.Restriction{Type: mapi.ResOr, Value: append([]mapi.Restriction(nil), conds...)}
}

// RuleNot negates a condition (a ResNot node): the rule matches when the inner
// condition does NOT match. Used to express a rule exception ("except when ...").
func RuleNot(cond mapi.Restriction) mapi.Restriction {
	return mapi.Restriction{Type: mapi.ResNot, Value: cond}
}

// contentContains builds a case-insensitive substring ResContent on tag.
func contentContains(tag mapi.PropTag, text string) mapi.Restriction {
	return mapi.Restriction{Type: mapi.ResContent, Value: mapi.ContentRestriction{
		FuzzyLevel: ruleFuzzyContains,
		PropTag:    tag,
		PropVal:    mapi.TaggedPropVal{Tag: tag, Value: text},
	}}
}

// RuleImportanceIs matches messages whose PR_IMPORTANCE equals level
// (mapi.ImportanceLow/Normal/High).
func RuleImportanceIs(level int) mapi.Restriction {
	return mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop:   mapi.RelopEQ,
		PropTag: mapi.PrImportance,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrImportance, Value: int32(level)},
	}}
}

// RuleSensitivityIs matches messages whose PR_SENSITIVITY equals level
// (None/Personal/Private/Confidential). The MIME import sets PR_SENSITIVITY, so
// the condition tests a property the delivered message carries.
func RuleSensitivityIs(level int) mapi.Restriction {
	return mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop:   mapi.RelopEQ,
		PropTag: mapi.PrSensitivity,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrSensitivity, Value: int32(level)},
	}}
}

// RuleSizeAtLeast matches messages of at least the given byte size.
func RuleSizeAtLeast(bytes int) mapi.Restriction {
	return mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop:   mapi.RelopGE,
		PropTag: mapi.PrMessageSize,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrMessageSize, Value: int32(bytes)},
	}}
}

// RuleMarkReadAction marks a matching message as read.
func RuleMarkReadAction() mapi.ActionBlock { return mapi.ActionBlock{Type: mapi.OpMarkAsRead} }

// RuleDeleteAction moves a matching message to Deleted Items.
func RuleDeleteAction() mapi.ActionBlock { return mapi.ActionBlock{Type: mapi.OpDelete} }

// RuleMoveAction moves a matching message to the target folder. The destination
// is carried in a same-store MoveCopyAction whose SVREID holds the folder id.
func RuleMoveAction(targetFolderID int64) mapi.ActionBlock {
	return mapi.ActionBlock{Type: mapi.OpMove, Data: mapi.MoveCopyAction{
		SameStore: true,
		FolderEID: mapi.SVREID{FolderID: mapi.EID(uint64(targetFolderID))},
	}}
}

// RuleCopyAction copies a matching message to the target folder, leaving the
// original in place. The destination is carried the same way as a move.
func RuleCopyAction(targetFolderID int64) mapi.ActionBlock {
	return mapi.ActionBlock{Type: mapi.OpCopy, Data: mapi.MoveCopyAction{
		SameStore: true,
		FolderEID: mapi.SVREID{FolderID: mapi.EID(uint64(targetFolderID))},
	}}
}

// ForwardRequest is a forward a matched rule asked for: the addresses to send to.
// The store collects these — it cannot send mail itself — and returns them to the
// delivery path, which sends the ORIGINAL received message (not the store's
// re-exported copy, which drops headers) after its loop/abuse guards. RunRules
// ("apply rules now") discards them on purpose.
type ForwardRequest struct {
	To []string
}

// RuleForwardAction forwards (redirects) a matching message to the given
// addresses: an OpForward block whose recipients carry their SMTP addresses.
func RuleForwardAction(addresses ...string) mapi.ActionBlock {
	recips := make([]mapi.RecipientBlock, 0, len(addresses))
	for _, a := range addresses {
		recips = append(recips, mapi.RecipientBlock{PropVals: []mapi.TaggedPropVal{
			{Tag: mapi.PrSmtpAddress, Value: a},
		}})
	}
	return mapi.ActionBlock{Type: mapi.OpForward, Data: mapi.ForwardDelegateAction{Recipients: recips}}
}

// RuleTagAction sets a property on a matching message (an OpTag block). The editor
// uses it to categorize: tag is the named Keywords property, values the categories.
func RuleTagAction(tag mapi.PropTag, values ...string) mapi.ActionBlock {
	return mapi.ActionBlock{Type: mapi.OpTag, Data: mapi.TaggedPropVal{Tag: tag, Value: values}}
}

// KeywordsPropTag resolves (creating if needed) the named Keywords property tag —
// the multi-value string property that holds a message's categories — so the rule
// editor can build a categorize action whose OpTag sets the same property the
// categories UI reads.
func (s *Store) KeywordsPropTag() (mapi.PropTag, error) {
	tag, _, err := s.namedProptag(mapi.NameKeywords, mapi.PtMvUnicode, true)
	return tag, err
}

// forwardAddresses extracts the SMTP addresses from an OpForward action's data.
func forwardAddresses(data any) []string {
	fd, ok := data.(mapi.ForwardDelegateAction)
	if !ok {
		return nil
	}
	var out []string
	for _, rb := range fd.Recipients {
		for _, pv := range rb.PropVals {
			if pv.Tag == mapi.PrSmtpAddress {
				if s, ok := pv.Value.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}
