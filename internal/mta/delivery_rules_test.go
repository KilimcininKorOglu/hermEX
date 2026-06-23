package mta

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"

	_ "modernc.org/sqlite"
)

// TestDeliverForwardsViaRuleWithGuards proves the delivery-time forward action
// sends through the OnRuleForward hook for ordinary mail (with the loop-break
// marker stamped), and that the guards suppress forwarding a bounce, an
// auto-submitted message, and an already-forwarded one — so a rule cannot become
// a backscatter or mail-loop source.
func TestDeliverForwardsViaRuleWithGuards(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRule(objectstore.Rule{
		FolderID: int64(mapi.PrivateFIDInbox), Name: "forward urgent", State: mapi.RuleStateEnabled,
		Condition: objectstore.RuleSubjectContains("urgent"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleForwardAction("boss@example.com")}},
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	type capture struct {
		owner string
		to    []string
		raw   []byte
	}
	var got []capture
	prev := OnRuleForward
	OnRuleForward = func(owner string, to []string, raw []byte) {
		got = append(got, capture{owner, to, raw})
	}
	t.Cleanup(func() { OnRuleForward = prev })

	deliverMsg := func(from, subject, extra string) {
		raw := "From: " + from + "\r\nTo: alice@test\r\nSubject: " + subject + "\r\n"
		if extra != "" {
			raw += extra + "\r\n"
		}
		raw += "\r\nbody\r\n"
		envFrom := from
		if subject == "urgent bounce" {
			envFrom = "" // a bounce: null envelope sender
		}
		if err := deliver(nil, envFrom, "alice@test", mbox, []byte(raw), time.Now(), int64(mapi.PrivateFIDInbox)); err != nil {
			t.Fatalf("deliver(%q): %v", subject, err)
		}
	}

	// 1. Ordinary matching mail forwards, with the marker stamped.
	deliverMsg("sender@example.com", "urgent ping", "")
	if len(got) != 1 {
		t.Fatalf("ordinary message: got %d forwards, want 1", len(got))
	}
	if got[0].owner != "alice@test" || len(got[0].to) != 1 || got[0].to[0] != "boss@example.com" {
		t.Errorf("forward owner/to = %q/%v, want alice@test/[boss@example.com]", got[0].owner, got[0].to)
	}
	if !bytes.Contains(got[0].raw, []byte(forwardMarkerHeader)) {
		t.Errorf("forwarded copy missing the loop-break marker header")
	}

	// 2-4. Each guarded message must NOT forward.
	for _, c := range []struct {
		name, subject, extra string
	}{
		{"bounce", "urgent bounce", ""},
		{"auto-submitted", "urgent auto", "Auto-Submitted: auto-generated"},
		{"already-forwarded", "urgent loop", forwardMarkerHeader + ": someone@else"},
	} {
		got = nil
		deliverMsg("sender@example.com", c.subject, c.extra)
		if len(got) != 0 {
			t.Errorf("%s message was forwarded (%d), want 0 (guard must suppress it)", c.name, len(got))
		}
	}
}

// TestDeliverAppliesInboxRules proves the delivery path runs inbox rules: a
// move rule fires on a matching incoming message, so it is filed in the target
// folder instead of sitting in the inbox, and delivery itself still succeeds.
func TestDeliverAppliesInboxRules(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	filed, err := st.CreateFolder(nil, "Filed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRule(objectstore.Rule{
		FolderID: int64(mapi.PrivateFIDInbox), Name: "file invoices", State: mapi.RuleStateEnabled,
		Condition: objectstore.RuleSubjectContains("invoice"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleMoveAction(filed)}},
	}); err != nil {
		t.Fatal(err)
	}
	st.Close() // deliver opens its own handle

	raw := []byte("From: billing@acme.com\r\nTo: alice@test\r\nSubject: your invoice is ready\r\n\r\nbody\r\n")
	// nil accounts: this mailbox has out-of-office off, so the auto-reply pass
	// returns before it would consult the directory.
	if err := deliver(nil, "billing@acme.com", "alice@test", mbox, raw, time.Now(), int64(mapi.PrivateFIDInbox)); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	st2, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	inbox, err := st2.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 0 {
		t.Errorf("inbox has %d messages, want 0 (the rule moved it)", len(inbox))
	}
	filedMsgs, err := st2.ListMessages(filed)
	if err != nil {
		t.Fatal(err)
	}
	if len(filedMsgs) != 1 {
		t.Errorf("Filed has %d messages, want 1 (moved by the delivery-time rule)", len(filedMsgs))
	}
}

// TestDeliverSurvivesMalformedRule is the resilience guarantee: a corrupt rule
// in the store must not fail delivery. A delivery that returned an error would
// make the sender retry and double-deliver, so deliver swallows the rule error
// and the message lands in the inbox regardless.
func TestDeliverSurvivesMalformedRule(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	st.Close() // provisioned; now corrupt a rule directly

	db, err := sql.Open("sqlite", "file:"+filepath.Join(mbox, "objects.sqlite3")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	// 0x03 = ResContent with no payload: an undecodable RESTRICTION blob.
	if _, err := db.Exec(
		`INSERT INTO rules (provider, sequence, state, condition, actions, folder_id) VALUES (?,?,?,?,?,?)`,
		"RuleOrganizer", 1, 1, []byte{0x03}, []byte{0x00}, int64(mapi.PrivateFIDInbox)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	raw := []byte("From: x@y.test\r\nTo: alice@test\r\nSubject: hello\r\n\r\nbody\r\n")
	// nil accounts: out-of-office is off here, so the auto-reply pass returns
	// before it would consult the directory.
	if err := deliver(nil, "x@y.test", "alice@test", mbox, raw, time.Now(), int64(mapi.PrivateFIDInbox)); err != nil {
		t.Fatalf("deliver must not fail on a malformed rule, got: %v", err)
	}

	st2, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	inbox, err := st2.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Errorf("inbox has %d messages, want 1 (delivery must survive a rule error)", len(inbox))
	}
}
