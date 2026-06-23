package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// sampleBag is the property bag of a representative delivered message, used to
// drive the pure-evaluator tests. The size is supplied separately at evaluation
// in production (injected as PR_MESSAGE_SIZE); here it is set directly.
func sampleBag() mapi.PropertyValues {
	return mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Quarterly Invoice 2026"},
		{Tag: mapi.PrSenderSmtpAddress, Value: "billing@acme.com"},
		{Tag: mapi.PrImportance, Value: int32(mapi.ImportanceHigh)},
		{Tag: mapi.PrMessageSize, Value: int32(50000)},
	}
}

// TestEvalRestrictionLeaves checks each curated condition matches the right
// messages and, crucially, that an absent property fails the leaf instead of
// matching or panicking — the difference between a rule that fires correctly and
// one that fires on everything or nothing.
func TestEvalRestrictionLeaves(t *testing.T) {
	bag := sampleBag()
	cases := []struct {
		name string
		r    mapi.Restriction
		want bool
	}{
		{"subject contains (case-insensitive)", RuleSubjectContains("invoice"), true},
		{"subject contains miss", RuleSubjectContains("refund"), false},
		{"from contains domain", RuleFromContains("acme.com"), true},
		{"from contains miss", RuleFromContains("gmail.com"), false},
		{"importance is high", RuleImportanceIs(mapi.ImportanceHigh), true},
		{"importance is low", RuleImportanceIs(mapi.ImportanceLow), false},
		{"size at least below", RuleSizeAtLeast(10000), true},
		{"size at least above", RuleSizeAtLeast(100000), false},
		{"exists present", mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{PropTag: mapi.PrSubject}}, true},
		{"exists absent", mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{PropTag: mapi.PrBody}}, false},
		{"content present match", RuleSubjectContains("2026"), true},
		{"content on missing property", contentContains(mapi.PrBody, "anything"), false},
		{"null matches all", mapi.Restriction{Type: mapi.ResNull}, true},
		{"unsupported sub is no match", mapi.Restriction{Type: mapi.ResSub}, false},
	}
	for _, c := range cases {
		if got := evalRestriction(c.r, bag); got != c.want {
			t.Errorf("%s: evalRestriction = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestEvalBodyAndSensitivity checks the body-contains and sensitivity-is
// conditions against a message that carries PR_BODY and PR_SENSITIVITY (both set
// by the MIME import), so the editor's newer conditions match real delivered mail
// rather than silently never firing.
func TestEvalBodyAndSensitivity(t *testing.T) {
	bag := mapi.PropertyValues{
		{Tag: mapi.PrBody, Value: "Please review the attached contract before Friday."},
		{Tag: mapi.PrSensitivity, Value: int32(mapi.SensitivityPrivate)},
	}
	cases := []struct {
		name string
		r    mapi.Restriction
		want bool
	}{
		{"body contains (case-insensitive)", RuleBodyContains("CONTRACT"), true},
		{"body contains miss", RuleBodyContains("invoice"), false},
		{"sensitivity is private", RuleSensitivityIs(mapi.SensitivityPrivate), true},
		{"sensitivity is confidential", RuleSensitivityIs(mapi.SensitivityConfidential), false},
	}
	for _, c := range cases {
		if got := evalRestriction(c.r, bag); got != c.want {
			t.Errorf("%s: evalRestriction = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestEvalRestrictionTree checks the boolean combinators compose the leaves
// correctly, including De Morgan-style cases and an empty AND/OR.
func TestEvalRestrictionTree(t *testing.T) {
	bag := sampleBag()
	hit := RuleSubjectContains("invoice") // true on the sample
	miss := RuleSubjectContains("refund") // false on the sample
	and := func(rs ...mapi.Restriction) mapi.Restriction {
		return mapi.Restriction{Type: mapi.ResAnd, Value: rs}
	}
	or := func(rs ...mapi.Restriction) mapi.Restriction {
		return mapi.Restriction{Type: mapi.ResOr, Value: rs}
	}
	not := func(r mapi.Restriction) mapi.Restriction {
		return mapi.Restriction{Type: mapi.ResNot, Value: r}
	}
	cases := []struct {
		name string
		r    mapi.Restriction
		want bool
	}{
		{"and all true", and(hit, RuleFromContains("acme.com")), true},
		{"and one false", and(hit, miss), false},
		{"or one true", or(miss, hit), true},
		{"or all false", or(miss, RuleFromContains("gmail.com")), false},
		{"not false is true", not(miss), true},
		{"not true is false", not(hit), false},
		{"empty and is true", and(), true},
		{"empty or is false", or(), false},
	}
	for _, c := range cases {
		if got := evalRestriction(c.r, bag); got != c.want {
			t.Errorf("%s: evalRestriction = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestEvalContentFuzzyKinds checks the fuzzy match kinds and case handling: a
// substring rule ignores case, a prefix rule anchors at the start, and a
// full-string rule requires the whole value to equal.
func TestEvalContentFuzzyKinds(t *testing.T) {
	bag := mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "Hello World"}}
	content := func(fuzzy uint32, needle string) mapi.Restriction {
		return mapi.Restriction{Type: mapi.ResContent, Value: mapi.ContentRestriction{
			FuzzyLevel: fuzzy, PropTag: mapi.PrSubject,
			PropVal: mapi.TaggedPropVal{Tag: mapi.PrSubject, Value: needle}}}
	}
	cases := []struct {
		name  string
		fuzzy uint32
		ndl   string
		want  bool
	}{
		{"substring ignorecase", flSubstring | flIgnoreCase, "world", true},
		{"substring case-sensitive miss", flSubstring, "world", false},
		{"substring case-sensitive hit", flSubstring, "World", true},
		{"prefix hit", flPrefix | flIgnoreCase, "hello", true},
		{"prefix miss", flPrefix | flIgnoreCase, "world", false},
		{"fullstring hit", flFullString | flIgnoreCase, "hello world", true},
		{"fullstring miss", flFullString, "Hello", false},
	}
	for _, c := range cases {
		if got := evalRestriction(content(c.fuzzy, c.ndl), bag); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// deliverTo files a raw RFC822 message into a folder and returns its index info,
// running the same Import path delivery uses so the property bag under test is
// exactly the one production rules see.
func deliverTo(t *testing.T, s *Store, fid int64, raw string) MessageInfo {
	t.Helper()
	info, err := s.AppendMessage(fid, []byte(raw), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func ruleMsg(subject, from, extraHeader string) string {
	h := "From: " + from + "\r\nTo: bob@hermex.test\r\nSubject: " + subject + "\r\n"
	if extraHeader != "" {
		h += extraHeader + "\r\n"
	}
	return h + "Content-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n"
}

// TestRuleCopyActionIsNonTerminal checks the copy action duplicates a matching
// message to the target folder while leaving the original in place, and — being
// non-terminal — does not stop a later rule from acting on the original.
func TestRuleCopyActionIsNonTerminal(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	filed, err := s.CreateFolder(nil, "Filed")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	m := deliverTo(t, s, inbox, ruleMsg("Project update", "lead@acme.com", ""))

	// rule 1: subject contains "project" -> copy to Filed (non-terminal)
	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "copy projects", State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("project"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleCopyAction(filed)}},
	}); err != nil {
		t.Fatalf("AddRule copy: %v", err)
	}
	// rule 2: subject contains "project" -> mark read (runs only if copy is non-terminal)
	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "read projects", State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("project"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMarkReadAction()}},
	}); err != nil {
		t.Fatalf("AddRule markread: %v", err)
	}

	if _, err := s.RunRules(inbox); err != nil {
		t.Fatalf("RunRules: %v", err)
	}

	// The original stays in the inbox (copy does not remove it)...
	if inboxMsgs, err := s.ListMessages(inbox); err != nil {
		t.Fatal(err)
	} else if len(inboxMsgs) != 1 {
		t.Fatalf("inbox has %d messages, want 1 (copy must keep the original)", len(inboxMsgs))
	}
	// ...the later rule still ran on it (copy was non-terminal)...
	if fl, _ := s.MessageFlags(inbox, m.UID); fl&FlagSeen == 0 {
		t.Errorf("original not marked read: copy was wrongly treated as terminal")
	}
	// ...and a duplicate landed in Filed.
	if filedMsgs, err := s.ListMessages(filed); err != nil {
		t.Fatal(err)
	} else if len(filedMsgs) != 1 {
		t.Errorf("Filed has %d messages, want 1 copy", len(filedMsgs))
	}
}

// TestRuleExitLevelStopsProcessing checks that a rule carrying the exit-level
// (stop-processing) state prevents a later matching rule from running: rule 1
// marks read and stops, so rule 2's move never fires.
func TestRuleExitLevelStopsProcessing(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	filed, err := s.CreateFolder(nil, "Filed")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	m := deliverTo(t, s, inbox, ruleMsg("urgent ping", "x@y.com", ""))

	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "stop", State: mapi.RuleStateEnabled | mapi.RuleStateExitLevel,
		Condition: RuleSubjectContains("urgent"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMarkReadAction()}},
	}); err != nil {
		t.Fatalf("AddRule 1: %v", err)
	}
	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "move", State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("urgent"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMoveAction(filed)}},
	}); err != nil {
		t.Fatalf("AddRule 2: %v", err)
	}

	if _, err := s.RunRules(inbox); err != nil {
		t.Fatalf("RunRules: %v", err)
	}

	if fl, _ := s.MessageFlags(inbox, m.UID); fl&FlagSeen == 0 {
		t.Errorf("the stop rule did not mark the message read")
	}
	if msgs, _ := s.ListMessages(inbox); len(msgs) != 1 {
		t.Errorf("inbox has %d, want 1 (the later move must not run after a stop rule)", len(msgs))
	}
	if msgs, _ := s.ListMessages(filed); len(msgs) != 0 {
		t.Errorf("Filed has %d, want 0 (no rule should run after exit-level)", len(msgs))
	}
}

// TestRunRulesEndToEnd is the discriminating test: it delivers real messages
// through the real Import path, then runs rules and asserts on the actual store
// state — a message moved to the target folder, another marked read, an
// unmatched one untouched. It would fail if Import stored the subject or sender
// under a tag the evaluator does not read, which a pure-evaluator test cannot
// catch. It also locks the terminal-action invariant: a message a move rule
// claims is not re-evaluated by a later rule.
func TestRunRulesEndToEnd(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	filed, err := s.CreateFolder(nil, "Filed")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	a := deliverTo(t, s, inbox, ruleMsg("Quarterly Invoice 12345", "billing@acme.com", ""))
	b := deliverTo(t, s, inbox, ruleMsg("lunch?", "bob@example.com", ""))
	c := deliverTo(t, s, inbox, ruleMsg("Weekly newsletter", "news@promo.com", ""))
	d := deliverTo(t, s, inbox, ruleMsg("Invoice from promo", "promo@vendor.com", ""))

	// rule 1 (sequence 1): subject contains "invoice" -> move to Filed (terminal)
	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "file invoices", State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("invoice"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMoveAction(filed)}},
	}); err != nil {
		t.Fatalf("AddRule 1: %v", err)
	}
	// rule 2 (sequence 2): from contains "promo" -> mark read
	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "read promos", State: mapi.RuleStateEnabled,
		Condition: RuleFromContains("promo"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMarkReadAction()}},
	}); err != nil {
		t.Fatalf("AddRule 2: %v", err)
	}

	res, err := s.RunRules(inbox)
	if err != nil {
		t.Fatalf("RunRules: %v", err)
	}
	if res.Evaluated != 4 {
		t.Errorf("evaluated = %d, want 4", res.Evaluated)
	}
	// a (invoice) moved, c (promo) marked read, d (invoice+promo) moved -> 3 acted.
	if res.Affected != 3 {
		t.Errorf("affected = %d, want 3", res.Affected)
	}

	// Filed now holds the two invoice messages (a and d); inbox holds b and c.
	filedMsgs, err := s.ListMessages(filed)
	if err != nil {
		t.Fatal(err)
	}
	if len(filedMsgs) != 2 {
		t.Fatalf("Filed has %d messages, want 2 (the two invoices)", len(filedMsgs))
	}
	inboxMsgs, err := s.ListMessages(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(inboxMsgs) != 2 {
		t.Fatalf("inbox has %d messages, want 2 (lunch + newsletter)", len(inboxMsgs))
	}

	// a and d must be gone from the inbox (moved by the terminal move rule).
	for _, m := range inboxMsgs {
		if m.UID == a.UID || m.UID == d.UID {
			t.Errorf("a moved/invoice message uid %d still in inbox", m.UID)
		}
	}

	// c (promo) stayed in the inbox and is now read; b is untouched and unread.
	cInfo, err := s.MessageByUID(inbox, c.UID)
	if err != nil {
		t.Fatalf("newsletter (promo) message gone from inbox: %v", err)
	}
	if cInfo.Flags&FlagSeen == 0 {
		t.Errorf("promo message was not marked read by the rule")
	}
	bInfo, err := s.MessageByUID(inbox, b.UID)
	if err != nil {
		t.Fatalf("lunch message gone from inbox: %v", err)
	}
	if bInfo.Flags&FlagSeen != 0 {
		t.Errorf("unmatched lunch message was marked read")
	}
}

// TestApplyInboxRulesMalformedBlobIsError verifies that a corrupt stored rule
// (a condition blob that is not a valid RESTRICTION) surfaces as an error rather
// than a panic, and that the delivered message is preserved regardless — the
// delivery path relies on this so a corrupt rule cannot lose mail.
func TestApplyInboxRulesMalformedBlobIsError(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	m := deliverTo(t, s, inbox, ruleMsg("hello", "x@example.com", ""))

	// 0x03 is ResContent, which needs a fuzzy level + tag + value to follow; with
	// no payload bytes it cannot be decoded.
	if _, err := s.objdb.Exec(
		`INSERT INTO rules (provider, sequence, state, condition, actions, folder_id) VALUES (?,?,?,?,?,?)`,
		"RuleOrganizer", 1, int64(mapi.RuleStateEnabled), []byte{0x03}, []byte{0x00}, inbox); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ApplyInboxRules(m); err == nil {
		t.Errorf("ApplyInboxRules should surface a malformed rule blob as an error, got nil")
	}
	if _, err := s.MessageByUID(inbox, m.UID); err != nil {
		t.Errorf("message must survive a malformed-rule error, but it is gone: %v", err)
	}
}

// TestRunRulesSkipsDisabled verifies a disabled rule (ST_ENABLED clear) is not
// applied, while an enabled rule on the same folder still runs.
func TestRunRulesSkipsDisabled(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	m := deliverTo(t, s, inbox, ruleMsg("disabled rule target", "x@example.com", ""))

	// A disabled mark-read rule that matches the message.
	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "disabled", State: 0, // not enabled
		Condition: RuleSubjectContains("disabled"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMarkReadAction()}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.RunRules(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if res.Affected != 0 {
		t.Errorf("affected = %d, want 0 (rule is disabled)", res.Affected)
	}
	info, err := s.MessageByUID(inbox, m.UID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Flags&FlagSeen != 0 {
		t.Errorf("disabled rule marked the message read")
	}
}

// TestRuleMoveActionRoundTrip locks the move target encoding end-to-end through
// storage: a move action built for an arbitrary allocated folder id survives
// serialization and decodes back to the same folder id, so a stored move rule
// targets the folder the editor chose.
func TestRuleMoveActionRoundTrip(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	target, err := s.CreateFolder(nil, "Target")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "mv", State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("x"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMoveAction(target)}},
	}); err != nil {
		t.Fatal(err)
	}
	rules, err := s.ListRules(inbox)
	if err != nil || len(rules) != 1 {
		t.Fatalf("ListRules: %v len=%d", err, len(rules))
	}
	got, ok := moveTargetFolder(rules[0].Actions.Blocks[0].Data)
	if !ok {
		t.Fatalf("move action did not decode a target folder")
	}
	if got != target {
		t.Errorf("decoded move target = %d, want %d", got, target)
	}
}

// TestCompoundRuleRoundTrips is the de-risking test for multi-condition rules: a
// RuleAll(subject, from) condition must survive AddRule's serialization and
// ListRules' deserialization (ext must encode/decode the ResAnd node), then match
// only a message satisfying BOTH leaves.
func TestCompoundRuleRoundTrips(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	filed, err := s.CreateFolder(nil, "Filed")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	both := deliverTo(t, s, inbox, ruleMsg("Quarterly Invoice", "billing@acme.com", ""))
	subjOnly := deliverTo(t, s, inbox, ruleMsg("Quarterly Invoice", "other@example.com", ""))

	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "both", State: mapi.RuleStateEnabled,
		Condition: RuleAll(RuleSubjectContains("invoice"), RuleFromContains("acme.com")),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMoveAction(filed)}},
	}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	// The stored rule's condition is the ResAnd node after a serialization round-trip.
	rules, err := s.ListRules(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Condition.Type != mapi.ResAnd {
		t.Fatalf("stored condition did not round-trip as ResAnd: %+v", rules[0].Condition)
	}

	if _, err := s.RunRules(inbox); err != nil {
		t.Fatalf("RunRules: %v", err)
	}
	// Only the both-match message moved to Filed; the subject-only one stayed.
	if msgs, _ := s.ListMessages(filed); len(msgs) != 1 {
		t.Errorf("Filed has %d, want 1 (only the AND-match moves)", len(msgs))
	}
	if _, err := s.MessageByUID(inbox, subjOnly.UID); err != nil {
		t.Errorf("subject-only message should remain in inbox: %v", err)
	}
	_ = both
}

// TestRuleExceptionRoundTrips de-risks the rule-exception form: a
// RuleAll(subject, RuleNot(from)) condition must survive serialization (ext must
// encode/decode the ResNot node) and match only a message whose subject matches
// AND whose sender does NOT match the exception.
func TestRuleExceptionRoundTrips(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	filed, err := s.CreateFolder(nil, "Filed")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	// Both have "invoice"; only the second is from the excepted sender.
	deliverTo(t, s, inbox, ruleMsg("Invoice 1", "vendor@example.com", ""))
	deliverTo(t, s, inbox, ruleMsg("Invoice 2", "billing@acme.com", ""))

	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "file invoices except acme", State: mapi.RuleStateEnabled,
		Condition: RuleAll(RuleSubjectContains("invoice"), RuleNot(RuleFromContains("acme.com"))),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMoveAction(filed)}},
	}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if rules, _ := s.ListRules(inbox); len(rules) != 1 || rules[0].Condition.Type != mapi.ResAnd {
		t.Fatalf("exception rule did not round-trip as a ResAnd condition")
	}
	if _, err := s.RunRules(inbox); err != nil {
		t.Fatalf("RunRules: %v", err)
	}
	// Only the non-excepted invoice moved.
	if msgs, _ := s.ListMessages(filed); len(msgs) != 1 {
		t.Errorf("Filed has %d, want 1 (the acme invoice is excepted)", len(msgs))
	}
	if msgs, _ := s.ListMessages(inbox); len(msgs) != 1 {
		t.Errorf("inbox has %d, want 1 (the excepted invoice stays)", len(msgs))
	}
}

// TestRuleForwardActionReturnsRequest checks a forward rule surfaces a
// ForwardRequest (the store cannot send mail) carrying the address and the
// message bytes, leaves the original in place (forward is non-terminal), and so
// also de-risks ext's OpForward serialization (the address must round-trip).
func TestRuleForwardActionReturnsRequest(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	m := deliverTo(t, s, inbox, ruleMsg("Project ping", "lead@acme.com", ""))

	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "forward projects", State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("project"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleForwardAction("boss@hermex.test")}},
	}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	forwards, err := s.ApplyInboxRules(m)
	if err != nil {
		t.Fatalf("ApplyInboxRules: %v", err)
	}
	if len(forwards) != 1 {
		t.Fatalf("got %d forward requests, want 1", len(forwards))
	}
	if len(forwards[0].To) != 1 || forwards[0].To[0] != "boss@hermex.test" {
		t.Errorf("forward To = %v, want [boss@hermex.test] (address must round-trip)", forwards[0].To)
	}
	if _, err := s.MessageByUID(inbox, m.UID); err != nil {
		t.Errorf("forwarded message should remain in inbox (forward is non-terminal): %v", err)
	}
}

// TestRuleTagActionSetsCategory de-risks the categorize action: an OpTag rule
// carrying the named Keywords property must survive serialization (ext encodes the
// multi-value TaggedPropVal) and, when applied, set the category the categories UI
// reads back via GetCategories. It is also non-terminal (message stays in inbox).
func TestRuleTagActionSetsCategory(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	m := deliverTo(t, s, inbox, ruleMsg("urgent ping", "x@y.com", ""))
	tag, err := s.KeywordsPropTag()
	if err != nil {
		t.Fatalf("KeywordsPropTag: %v", err)
	}
	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "tag urgent", State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("urgent"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleTagAction(tag, "Important")}},
	}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if _, err := s.RunRules(inbox); err != nil {
		t.Fatalf("RunRules: %v", err)
	}
	cats, err := s.GetCategories(m.ID)
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	if len(cats) != 1 || cats[0] != "Important" {
		t.Errorf("categories = %v, want [Important] (OpTag must round-trip and apply)", cats)
	}
	if _, err := s.MessageByUID(inbox, m.UID); err != nil {
		t.Errorf("categorized message should remain in inbox (tag is non-terminal): %v", err)
	}
}
