package ews

import (
	"encoding/xml"
	"net/http/httptest"
	"strconv"
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
