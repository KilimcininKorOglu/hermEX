package objectstore

import (
	"testing"

	"hermex/internal/mapi"
)

// TestRunRulesInSweepsAnotherFolder is the point of letting a run name its folder.
// Rules are stored on the Inbox, so a user who writes a rule after the fact has to
// be able to run it over the folder the old mail is actually in.
func TestRunRulesInSweepsAnotherFolder(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	archive := mustCreateFolder(t, s, nil, "Archive")
	filed := mustCreateFolder(t, s, nil, "Filed")
	deliverTo(t, s, archive, ruleMsg("Project update", "lead@acme.com", ""))

	mustAddRule(t, s, Rule{
		FolderID: inbox, Name: "file projects", State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("project"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMoveAction(filed)}},
	})

	// Running over the Inbox touches nothing: the message is not there.
	res, err := s.RunRules(inbox, 0)
	mustNoErr(t, "run rules", err)
	wantEq(t, "messages the inbox run affected", res.Affected, 0)

	res, err = s.RunRulesIn(RunRulesOptions{RuleFolderID: inbox, TargetFolderID: archive}, 0)
	mustNoErr(t, "run rules in", err)
	wantEq(t, "archive run evaluated", res.Evaluated, 1)
	wantEq(t, "archive run affected", res.Affected, 1)
	wantEq(t, "messages in Filed", len(mustListMessages(t, s, filed)), 1)
}

// TestRunRulesInRunsOnlyTheNamedRule is the "run selected" case. A user who fixes
// one rule wants that rule applied, not every other one as a side effect.
func TestRunRulesInRunsOnlyTheNamedRule(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	filed, err := s.CreateFolder(nil, "Filed")
	if err != nil {
		t.Fatal(err)
	}
	m := deliverTo(t, s, inbox, ruleMsg("Project update", "lead@acme.com", ""))

	markID, err := s.AddRule(Rule{
		FolderID: inbox, Name: "mark projects", Sequence: 0, State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("project"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMarkReadAction()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "file projects", Sequence: 1, State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("project"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMoveAction(filed)}},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := s.RunRulesIn(RunRulesOptions{RuleFolderID: inbox, TargetFolderID: inbox, RuleID: markID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Affected != 1 {
		t.Fatalf("affected %d, want 1", res.Affected)
	}
	if fl, _ := s.MessageFlags(inbox, m.UID); fl&FlagSeen == 0 {
		t.Error("the named rule did not run")
	}
	if msgs, err := s.ListMessages(filed); err != nil {
		t.Fatal(err)
	} else if len(msgs) != 0 {
		t.Errorf("Filed holds %d messages: the rule that was not named ran too", len(msgs))
	}
}

// TestRunRulesInIgnoresAnUnknownRule keeps a stale selection from becoming a
// run-everything: a filter whose rule no longer exists must touch nothing.
func TestRunRulesInIgnoresAnUnknownRule(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	m := deliverTo(t, s, inbox, ruleMsg("Project update", "lead@acme.com", ""))

	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "mark projects", State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("project"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMarkReadAction()}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.RunRulesIn(RunRulesOptions{RuleFolderID: inbox, TargetFolderID: inbox, RuleID: 99999}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Affected != 0 {
		t.Errorf("affected %d, want 0", res.Affected)
	}
	if fl, _ := s.MessageFlags(inbox, m.UID); fl&FlagSeen != 0 {
		t.Error("an unknown rule id fell back to running every rule")
	}
}

// TestRunRulesInSkipsAMoveOntoItsOwnFolder is the self-move guard. Sweeping the
// folder a rule files into would otherwise re-append every message under a fresh
// UID and delete the original, invalidating every client's cache for nothing.
func TestRunRulesInSkipsAMoveOntoItsOwnFolder(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	filed, err := s.CreateFolder(nil, "Filed")
	if err != nil {
		t.Fatal(err)
	}
	before := deliverTo(t, s, filed, ruleMsg("Project update", "lead@acme.com", ""))

	if _, err := s.AddRule(Rule{
		FolderID: inbox, Name: "file projects", State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("project"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{RuleMoveAction(filed)}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunRulesIn(RunRulesOptions{RuleFolderID: inbox, TargetFolderID: filed}, 0); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.ListMessages(filed)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("Filed holds %d messages, want 1", len(msgs))
	}
	if msgs[0].UID != before.UID {
		t.Errorf("UID changed from %d to %d: the message was moved onto its own folder", before.UID, msgs[0].UID)
	}
}
