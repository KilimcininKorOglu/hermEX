package mta

import (
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// oofPair provisions alice and bob, turns bob's out-of-office on, and returns the
// accounts with both mailbox paths.
func oofPair(t *testing.T) (directory.StaticAccounts, string, string) {
	t.Helper()
	dir := t.TempDir()
	pathA, pathB := filepath.Join(dir, "alice"), filepath.Join(dir, "bob")
	st, err := objectstore.Open(pathA)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	enableOOF(t, pathB)
	return directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: pathA},
		"bob@hermex.test":   {MailboxPath: pathB},
	}, pathA, pathB
}

func mailBob(t *testing.T, accounts directory.Accounts, pathB, subject string) {
	t.Helper()
	raw := []byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: " + subject + "\r\n\r\nhello\r\n")
	if err := deliver(accounts, "alice@hermex.test", "bob@hermex.test", pathB, raw, when, int64(mapi.PrivateFIDInbox)); err != nil {
		t.Fatalf("deliver: %v", err)
	}
}

func setBobOOF(t *testing.T, pathB string, cfg objectstore.OOFSettings) {
	t.Helper()
	st, err := objectstore.Open(pathB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetOOFSettings(cfg); err != nil {
		t.Fatal(err)
	}
}

// TestOOFRepliesOncePerSender: RFC 3834 section 2 says an automatic responder
// should not reply to the same sender more than once in a period, and Exchange
// answers each sender once while out-of-office stays on. Three messages from
// alice draw one reply.
func TestOOFRepliesOncePerSender(t *testing.T) {
	accounts, pathA, pathB := oofPair(t)
	for _, s := range []string{"one", "two", "three"} {
		mailBob(t, accounts, pathB, s)
	}
	if n := len(listInbox(t, pathA)); n != 1 {
		t.Errorf("alice received %d auto-replies for three messages, want 1", n)
	}
}

// TestOOFHistoryIsPerSender: the history names each sender, it is not one
// "already replied" switch, and an address is one sender whatever its case
// (RFC 5321 2.4 makes the domain case-insensitive, and the server treats the
// local part the same way).
func TestOOFHistoryIsPerSender(t *testing.T) {
	_, pathA, pathB := oofPair(t)
	pathC := filepath.Join(filepath.Dir(pathA), "carol")
	st, err := objectstore.Open(pathC)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: pathA},
		"bob@hermex.test":   {MailboxPath: pathB},
		"carol@hermex.test": {MailboxPath: pathC},
	}
	for _, from := range []string{"alice@hermex.test", "Alice@Hermex.Test", "carol@hermex.test", "carol@hermex.test"} {
		raw := []byte("From: " + from + "\r\nTo: bob@hermex.test\r\nSubject: hi\r\n\r\nhello\r\n")
		if err := deliver(accounts, from, "bob@hermex.test", pathB, raw, when, int64(mapi.PrivateFIDInbox)); err != nil {
			t.Fatalf("deliver from %s: %v", from, err)
		}
	}
	if n := len(listInbox(t, pathA)); n != 1 {
		t.Errorf("alice, writing in two cases, received %d replies, want 1", n)
	}
	if n := len(listInbox(t, pathC)); n != 1 {
		t.Errorf("carol, a second sender, received %d replies, want 1", n)
	}
}

// TestASuppressedMessageDoesNotUseUpTheReply: a message that draws no reply (it
// is automated, RFC 3834 section 2) must leave the sender unanswered, so their
// next ordinary message is answered.
func TestASuppressedMessageDoesNotUseUpTheReply(t *testing.T) {
	accounts, pathA, pathB := oofPair(t)
	auto := []byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nAuto-Submitted: auto-generated\r\nSubject: digest\r\n\r\nx\r\n")
	if err := deliver(accounts, "alice@hermex.test", "bob@hermex.test", pathB, auto, when, int64(mapi.PrivateFIDInbox)); err != nil {
		t.Fatal(err)
	}
	mailBob(t, accounts, pathB, "a real question")
	if n := len(listInbox(t, pathA)); n != 1 {
		t.Errorf("alice received %d replies, want 1 (to her ordinary message)", n)
	}
}

// TestAVacationRuleRepliesOncePerSender: a vacation rule is an automatic responder
// too and answers each sender once, while a reject rule answers every message,
// since each rejected message is a separate delivery failure.
func TestAVacationRuleRepliesOncePerSender(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bob")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []objectstore.Rule{
		{Name: "away", Condition: objectstore.RuleSubjectContains("spam"),
			Actions: mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleVacationAction("Away this week.")}}},
		{Name: "refuse", Condition: objectstore.RuleSubjectContains("spam"),
			Actions: mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleRejectAction("not wanted")}}},
	} {
		rule.FolderID, rule.State = int64(mapi.PrivateFIDInbox), mapi.RuleStateEnabled
		if _, err := st.AddRule(rule); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	var vacations, rejects int
	prev := OnRuleSend
	OnRuleSend = func(_ string, _ []string, raw []byte) {
		if strings.Contains(string(raw), "Away this week.") {
			vacations++
		} else {
			rejects++
		}
	}
	defer func() { OnRuleSend = prev }()
	for _, subject := range []string{"spam one", "spam two", "spam three"} {
		raw := []byte("From: carol@example.org\r\nTo: bob@hermex.test\r\nSubject: " + subject + "\r\n\r\nx\r\n")
		if err := deliver(nil, "carol@example.org", "bob@hermex.test", path, raw, when, int64(mapi.PrivateFIDInbox)); err != nil {
			t.Fatal(err)
		}
	}
	if vacations != 1 {
		t.Errorf("the vacation rule sent %d replies for three messages, want 1", vacations)
	}
	if rejects != 3 {
		t.Errorf("the reject rule sent %d reports for three messages, want 3", rejects)
	}
}

// TestOOFAnswersAgainInANewPeriod: turning out-of-office off ends the period, and
// a changed reply is a new message the sender has not seen, so each starts the
// sender list again.
func TestOOFAnswersAgainInANewPeriod(t *testing.T) {
	accounts, pathA, pathB := oofPair(t)
	mailBob(t, accounts, pathB, "one")

	setBobOOF(t, pathB, objectstore.OOFSettings{})
	setBobOOF(t, pathB, objectstore.OOFSettings{Enabled: true, InternalSubject: "Away", InternalReply: "Away, back later."})
	mailBob(t, accounts, pathB, "two")
	if n := len(listInbox(t, pathA)); n != 2 {
		t.Fatalf("after out-of-office was turned off and on, alice has %d replies, want 2", n)
	}

	setBobOOF(t, pathB, objectstore.OOFSettings{Enabled: true, InternalSubject: "Away", InternalReply: "Back on Monday."})
	mailBob(t, accounts, pathB, "three")
	if n := len(listInbox(t, pathA)); n != 3 {
		t.Errorf("after the reply text changed, alice has %d replies, want 3", n)
	}
}
