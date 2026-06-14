package mta

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"

	_ "modernc.org/sqlite"
)

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
	if err := deliver(nil, "billing@acme.com", "alice@test", mbox, raw, time.Now()); err != nil {
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
	if err := deliver(nil, "x@y.test", "alice@test", mbox, raw, time.Now()); err != nil {
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
