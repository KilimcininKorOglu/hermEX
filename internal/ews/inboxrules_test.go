package ews

import (
	"encoding/xml"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// seedRule adds a rule to the inbox and returns its id.
func seedRule(t *testing.T, dir string, r objectstore.Rule) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r.FolderID = int64(mapi.PrivateFIDInbox)
	id, err := st.AddRule(r)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func getInboxRulesReq() string {
	return wrapRequest(`<GetInboxRules xmlns="` + nsMessages + `"><MailboxSmtpAddress>` + testUser + `</MailboxSmtpAddress></GetInboxRules>`)
}

// wireRule is a namespace-agnostic view of a returned Rule.
type wireRule struct {
	RuleID         string `xml:"RuleId"`
	DisplayName    string `xml:"DisplayName"`
	Priority       int    `xml:"Priority"`
	IsEnabled      bool   `xml:"IsEnabled"`
	IsNotSupported bool   `xml:"IsNotSupported"`
	Conditions     struct {
		ContainsSubjectStrings []string `xml:"ContainsSubjectStrings>String"`
		ContainsSenderStrings  []string `xml:"ContainsSenderStrings>String"`
		Importance             string   `xml:"Importance"`
	} `xml:"Conditions"`
	Actions struct {
		Delete       bool `xml:"Delete"`
		MarkAsRead   bool `xml:"MarkAsRead"`
		MoveToFolder struct {
			FolderID struct {
				ID string `xml:"Id,attr"`
			} `xml:"FolderId"`
		} `xml:"MoveToFolder"`
	} `xml:"Actions"`
}

// getInboxRules posts a GetInboxRules and returns OutlookRuleBlobExists and the
// returned rules.
func getInboxRules(t *testing.T, ts *httptest.Server) (bool, []wireRule) {
	t.Helper()
	_, out := soapPost(t, ts, getInboxRulesReq(), true)
	var env struct {
		Blob  bool       `xml:"Body>GetInboxRulesResponse>OutlookRuleBlobExists"`
		Rules []wireRule `xml:"Body>GetInboxRulesResponse>InboxRules>Rule"`
	}
	if err := xml.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal GetInboxRules: %v\n%s", err, out)
	}
	return env.Blob, env.Rules
}

// TestGetInboxRulesMapsCuratedRule confirms a curated rule (subject-contains
// condition + move action) maps to the matching wire predicates and action.
func TestGetInboxRulesMapsCuratedRule(t *testing.T) {
	ts, dir := seededEWS(t)
	target := int64(mapi.PrivateFIDDeletedItems)
	id := seedRule(t, dir, objectstore.Rule{
		Name:      "Sale",
		Sequence:  1,
		State:     mapi.RuleStateEnabled,
		Condition: objectstore.RuleSubjectContains("sale"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleMoveAction(target)}},
	})

	blob, rules := getInboxRules(t, ts)
	if blob {
		t.Error("OutlookRuleBlobExists must be false")
	}
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(rules))
	}
	r := rules[0]
	if r.RuleID != itoa(id) || r.DisplayName != "Sale" || r.Priority != 1 || !r.IsEnabled || r.IsNotSupported {
		t.Errorf("rule header wrong: %+v", r)
	}
	if len(r.Conditions.ContainsSubjectStrings) != 1 || r.Conditions.ContainsSubjectStrings[0] != "sale" {
		t.Errorf("subject condition = %v, want [sale]", r.Conditions.ContainsSubjectStrings)
	}
	if r.Actions.MoveToFolder.FolderID.ID != oxews.EncodeFolderID(target) {
		t.Errorf("move target = %q, want %q", r.Actions.MoveToFolder.FolderID.ID, oxews.EncodeFolderID(target))
	}
}

// TestGetInboxRulesRecognizesAndOr confirms the read side folds an AND of curated
// leaves into one predicate set and an OR of same-tag content into an array.
func TestGetInboxRulesRecognizesAndOr(t *testing.T) {
	ts, dir := seededEWS(t)
	seedRule(t, dir, objectstore.Rule{
		Name:     "AndRule",
		Sequence: 1,
		State:    mapi.RuleStateEnabled,
		Condition: mapi.Restriction{Type: mapi.ResAnd, Value: []mapi.Restriction{
			objectstore.RuleSubjectContains("invoice"),
			objectstore.RuleFromContains("billing@"),
		}},
		Actions: mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleMarkReadAction()}},
	})
	seedRule(t, dir, objectstore.Rule{
		Name:     "OrRule",
		Sequence: 2,
		State:    mapi.RuleStateEnabled,
		Condition: mapi.Restriction{Type: mapi.ResOr, Value: []mapi.Restriction{
			objectstore.RuleSubjectContains("x"),
			objectstore.RuleSubjectContains("y"),
		}},
		Actions: mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleDeleteAction()}},
	})

	_, rules := getInboxRules(t, ts)
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rules))
	}
	and := rules[0]
	if len(and.Conditions.ContainsSubjectStrings) != 1 || and.Conditions.ContainsSubjectStrings[0] != "invoice" {
		t.Errorf("AND subject = %v", and.Conditions.ContainsSubjectStrings)
	}
	if len(and.Conditions.ContainsSenderStrings) != 1 || and.Conditions.ContainsSenderStrings[0] != "billing@" {
		t.Errorf("AND sender = %v", and.Conditions.ContainsSenderStrings)
	}
	if !and.Actions.MarkAsRead {
		t.Error("AND action must be MarkAsRead")
	}
	or := rules[1]
	if len(or.Conditions.ContainsSubjectStrings) != 2 {
		t.Errorf("OR subject = %v, want two strings", or.Conditions.ContainsSubjectStrings)
	}
	if !or.Actions.Delete {
		t.Error("OR action must be Delete")
	}
}

// TestGetInboxRulesMarksUnsupported confirms a rule whose condition is outside the
// curated vocabulary is listed but marked IsNotSupported with no Conditions/Actions.
func TestGetInboxRulesMarksUnsupported(t *testing.T) {
	ts, dir := seededEWS(t)
	// A content match on a tag the EWS surface does not map (PR_BODY) is recognized
	// by the evaluator but not by the curated wire vocabulary.
	seedRule(t, dir, objectstore.Rule{
		Name:      "Custom",
		Sequence:  1,
		State:     mapi.RuleStateEnabled,
		Condition: contentContainsTag(mapi.PrBody, "secret"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleMarkReadAction()}},
	})
	_, rules := getInboxRules(t, ts)
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(rules))
	}
	r := rules[0]
	if !r.IsNotSupported {
		t.Error("a rule with an unmapped condition must be IsNotSupported")
	}
	if r.DisplayName != "Custom" || r.RuleID == "" {
		t.Errorf("an unsupported rule must still carry its header: %+v", r)
	}
	if len(r.Conditions.ContainsSubjectStrings) != 0 || r.Actions.MarkAsRead {
		t.Error("an unsupported rule must not carry partial conditions/actions")
	}
}

// TestGetInboxRulesEmpty confirms a mailbox with no rules returns no InboxRules and
// OutlookRuleBlobExists false.
func TestGetInboxRulesEmpty(t *testing.T) {
	ts, _ := seededEWS(t)
	blob, rules := getInboxRules(t, ts)
	if blob || len(rules) != 0 {
		t.Errorf("empty mailbox: blob=%v rules=%d, want false/0", blob, len(rules))
	}
}

// contentContainsTag builds a substring content restriction on an arbitrary tag,
// for seeding a rule the curated wire vocabulary does not recognize.
func contentContainsTag(tag mapi.PropTag, text string) mapi.Restriction {
	return mapi.Restriction{Type: mapi.ResContent, Value: mapi.ContentRestriction{
		PropTag: tag,
		PropVal: mapi.TaggedPropVal{Tag: tag, Value: text},
	}}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// --- UpdateInboxRules (write) test helpers ---

func updateInboxRulesReq(ops string) string {
	return wrapRequest(`<UpdateInboxRules xmlns="` + nsMessages + `">` +
		`<MailboxSmtpAddress>` + testUser + `</MailboxSmtpAddress>` +
		`<Operations xmlns:t="` + nsTypes + `">` + ops + `</Operations>` +
		`</UpdateInboxRules>`)
}

func createRuleOp(rule string) string {
	return `<t:CreateRuleOperation><t:Rule>` + rule + `</t:Rule></t:CreateRuleOperation>`
}
func setRuleOp(rule string) string {
	return `<t:SetRuleOperation><t:Rule>` + rule + `</t:Rule></t:SetRuleOperation>`
}
func deleteRuleOp(id string) string {
	return `<t:DeleteRuleOperation><t:RuleId>` + id + `</t:RuleId></t:DeleteRuleOperation>`
}

// ruleBody builds a <t:Rule> body (the caller prepends a RuleId for a Set).
func ruleBody(name string, priority int, conds, actions string) string {
	return `<t:DisplayName>` + name + `</t:DisplayName>` +
		`<t:Priority>` + strconv.Itoa(priority) + `</t:Priority>` +
		`<t:IsEnabled>true</t:IsEnabled>` +
		`<t:Conditions>` + conds + `</t:Conditions>` +
		`<t:Actions>` + actions + `</t:Actions>`
}

func subjectCond(s string) string {
	return `<t:ContainsSubjectStrings><t:String>` + s + `</t:String></t:ContainsSubjectStrings>`
}

// storeRules opens the store and returns the inbox rules.
func storeRules(t *testing.T, dir string) []objectstore.Rule {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rules, err := st.ListRules(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

// opErrorIndexes parses the OperationIndex of each RuleOperationError.
func opErrorIndexes(t *testing.T, out string) []int {
	t.Helper()
	var env struct {
		Indexes []int `xml:"Body>UpdateInboxRulesResponse>RuleOperationErrors>RuleOperationError>OperationIndex"`
	}
	if err := xml.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal RuleOperationErrors: %v\n%s", err, out)
	}
	return env.Indexes
}

// TestUpdateInboxRulesCreateRoundTrip creates a curated rule via UpdateInboxRules
// and reads back the same predicates and action via GetInboxRules.
func TestUpdateInboxRulesCreateRoundTrip(t *testing.T) {
	ts, dir := seededEWS(t)
	target := oxews.EncodeFolderID(int64(mapi.PrivateFIDDeletedItems))
	actions := `<t:MoveToFolder><t:FolderId Id="` + target + `"/></t:MoveToFolder>`
	rule := ruleBody("Sale", 1, subjectCond("sale"), actions)

	resp, out := soapPost(t, ts, updateInboxRulesReq(createRuleOp(rule)), true)
	if resp.StatusCode != 200 || !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("Create not success (%d): %s", resp.StatusCode, out)
	}
	if rules := storeRules(t, dir); len(rules) != 1 || rules[0].Name != "Sale" {
		t.Fatalf("store has %d rules, want 1 named Sale", len(rules))
	}

	_, got := getInboxRules(t, ts)
	if len(got) != 1 {
		t.Fatalf("GetInboxRules returned %d rules, want 1", len(got))
	}
	r := got[0]
	if r.IsNotSupported {
		t.Error("a round-tripped curated rule must not be IsNotSupported")
	}
	if len(r.Conditions.ContainsSubjectStrings) != 1 || r.Conditions.ContainsSubjectStrings[0] != "sale" {
		t.Errorf("subject = %v, want [sale]", r.Conditions.ContainsSubjectStrings)
	}
	if r.Actions.MoveToFolder.FolderID.ID != target {
		t.Errorf("move target = %q, want %q", r.Actions.MoveToFolder.FolderID.ID, target)
	}
}

// TestUpdateInboxRulesSetDelete modifies then removes a rule, verifying each via
// GetInboxRules.
func TestUpdateInboxRulesSetDelete(t *testing.T) {
	ts, dir := seededEWS(t)
	id := seedRule(t, dir, objectstore.Rule{
		Name: "Orig", Sequence: 1, State: mapi.RuleStateEnabled,
		Condition: objectstore.RuleSubjectContains("a"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleDeleteAction()}},
	})

	setRule := `<t:RuleId>` + itoa(id) + `</t:RuleId>` +
		ruleBody("Renamed", 2, `<t:ContainsSenderStrings><t:String>spam@</t:String></t:ContainsSenderStrings>`, `<t:MarkAsRead>true</t:MarkAsRead>`)
	if _, out := soapPost(t, ts, updateInboxRulesReq(setRuleOp(setRule)), true); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("Set not success: %s", out)
	}
	_, got := getInboxRules(t, ts)
	if len(got) != 1 || got[0].DisplayName != "Renamed" {
		t.Fatalf("set did not rename: %+v", got)
	}
	if len(got[0].Conditions.ContainsSenderStrings) != 1 || !got[0].Actions.MarkAsRead {
		t.Errorf("set did not replace condition/action: %+v", got[0])
	}

	if _, out := soapPost(t, ts, updateInboxRulesReq(deleteRuleOp(itoa(id))), true); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("Delete not success: %s", out)
	}
	if _, got := getInboxRules(t, ts); len(got) != 0 {
		t.Errorf("delete left %d rules", len(got))
	}
}

// TestUpdateInboxRulesUnsupportedRejected confirms a predicate outside the curated
// vocabulary fails validation and stores nothing.
func TestUpdateInboxRulesUnsupportedRejected(t *testing.T) {
	ts, dir := seededEWS(t)
	rule := ruleBody("Bad", 1, `<t:HasAttachments>true</t:HasAttachments>`, `<t:Delete>true</t:Delete>`)
	_, out := soapPost(t, ts, updateInboxRulesReq(createRuleOp(rule)), true)
	if !strings.Contains(out, "ErrorInboxRulesValidationError") {
		t.Errorf("an unsupported predicate must be ErrorInboxRulesValidationError: %s", out)
	}
	if idx := opErrorIndexes(t, out); len(idx) != 1 || idx[0] != 0 {
		t.Errorf("operation error index = %v, want [0]", idx)
	}
	if rules := storeRules(t, dir); len(rules) != 0 {
		t.Errorf("an unsupported rule must not be stored, got %d", len(rules))
	}
}

// TestUpdateInboxRulesAtomic confirms a batch with one valid and one invalid
// operation applies neither (atomic), and reports the invalid one's index.
func TestUpdateInboxRulesAtomic(t *testing.T) {
	ts, dir := seededEWS(t)
	good := createRuleOp(ruleBody("Good", 1, subjectCond("x"), `<t:Delete>true</t:Delete>`))
	bad := createRuleOp(ruleBody("Bad", 2, `<t:HasAttachments>true</t:HasAttachments>`, `<t:Delete>true</t:Delete>`))
	_, out := soapPost(t, ts, updateInboxRulesReq(good+bad), true)
	if !strings.Contains(out, "ErrorInboxRulesValidationError") {
		t.Fatalf("a batch with an invalid op must fail: %s", out)
	}
	if idx := opErrorIndexes(t, out); len(idx) != 1 || idx[0] != 1 {
		t.Errorf("error index = %v, want [1] (the second op)", idx)
	}
	if rules := storeRules(t, dir); len(rules) != 0 {
		t.Errorf("atomic batch must store nothing, got %d rules", len(rules))
	}
}
