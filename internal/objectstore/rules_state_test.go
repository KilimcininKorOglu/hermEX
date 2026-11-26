package objectstore

import (
	"testing"

	"hermex/internal/mapi"
)

// stateRuleStore seeds a store with one move rule carrying the given PidTagRuleState, and
// returns the store plus the folder the rule moves into.
func stateRuleStore(t *testing.T, state uint32) (*Store, int64) {
	t.Helper()
	s := openSeededStore(t)
	filed, err := s.CreateFolder(nil, "Filed")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if _, err := s.AddRule(Rule{
		FolderID: int64(mapi.PrivateFIDInbox), Name: "file it", State: state,
		Condition: RuleSubjectContains("urgent"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMoveAction(filed)}},
	}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	return s, filed
}

// ruleMoved reports whether the rule moved a delivered message out of the inbox,
// failing when the message is in neither the inbox nor the rule's target.
func ruleMoved(t *testing.T, s *Store, filed int64) bool {
	t.Helper()
	inbox := int64(mapi.PrivateFIDInbox)
	m := deliverTo(t, s, inbox, ruleMsg("urgent", "x@y.com", ""))
	if _, err := s.ApplyInboxRules(m, 1000); err != nil {
		t.Fatalf("ApplyInboxRules: %v", err)
	}
	inInbox := len(mustListMessages(t, s, inbox))
	inFiled := len(mustListMessages(t, s, filed))
	if inInbox+inFiled != 1 {
		t.Fatalf("inbox holds %d and the target %d, want the one message in exactly one", inInbox, inFiled)
	}
	return inFiled == 1
}

// TestOnlyWhenOOFRuleWaitsForTheAwayState is the load-bearing case: Outlook sets
// ST_ONLY_WHEN_OOF on a rule the user wants only while away, so it must not act on mail
// the user is there to read.
func TestOnlyWhenOOFRuleWaitsForTheAwayState(t *testing.T) {
	s, filed := stateRuleStore(t, mapi.RuleStateEnabled|mapi.RuleStateOnlyWhenOOF)

	if ruleMoved(t, s, filed) {
		t.Error("an ST_ONLY_WHEN_OOF rule acted while the mailbox was not out of office")
	}
}

// TestOnlyWhenOOFRuleRunsWhileAway proves the same rule does act once the mailbox is out
// of office, so the bit gates the rule rather than disabling it.
func TestOnlyWhenOOFRuleRunsWhileAway(t *testing.T) {
	s, filed := stateRuleStore(t, mapi.RuleStateEnabled|mapi.RuleStateOnlyWhenOOF)
	if err := s.SetOOFSettings(OOFSettings{Enabled: true}); err != nil {
		t.Fatalf("SetOOFSettings: %v", err)
	}

	if !ruleMoved(t, s, filed) {
		t.Error("an ST_ONLY_WHEN_OOF rule did not act while the mailbox was out of office")
	}
}

// TestErroredRuleDoesNotRun proves a rule a client marked ST_ERROR stays out until the
// client clears the bit, because re-running a rule known to be broken would repeat the
// failure on every message.
func TestErroredRuleDoesNotRun(t *testing.T) {
	s, filed := stateRuleStore(t, mapi.RuleStateEnabled|mapi.RuleStateError)

	if ruleMoved(t, s, filed) {
		t.Error("a rule marked ST_ERROR still acted")
	}
}

// TestAnOrdinaryRuleStillRuns proves the state checks did not switch off a plain enabled
// rule.
func TestAnOrdinaryRuleStillRuns(t *testing.T) {
	s, filed := stateRuleStore(t, mapi.RuleStateEnabled)

	if !ruleMoved(t, s, filed) {
		t.Error("an enabled rule with no other state bits did not act")
	}
}
