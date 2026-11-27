package mta

import (
	"path/filepath"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestDeliverMeetingHookGatesAutoReply proves the delivery path defers to the meeting
// processor: when it handles a message (a meeting request) the out-of-office reply is
// skipped (the organizer gets a meeting response, not an OOF); when it does not, the
// out-of-office reply still fires for ordinary mail. The second direction is the
// load-bearing half, it proves wiring the processor in did not break OOF delivery.
func TestDeliverMeetingHookGatesAutoReply(t *testing.T) {
	for _, tc := range []struct {
		name        string
		handled     bool
		wantReplies int
	}{
		{"handled skips out-of-office", true, 0},
		{"not handled runs out-of-office", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := OnMeetingRequest
			OnMeetingRequest = func(*objectstore.Store, directory.Accounts, string, string, int64) bool { return tc.handled }
			t.Cleanup(func() { OnMeetingRequest = prev })

			dir := t.TempDir()
			pathA := filepath.Join(dir, "alice")
			pathB := filepath.Join(dir, "bob")
			enableOOF(t, pathB)
			if st, err := objectstore.Open(pathA); err != nil {
				t.Fatal(err)
			} else {
				mustClose(t, st)
			}
			accounts := directory.StaticAccounts{
				"alice@hermex.test": {MailboxPath: pathA},
				"bob@hermex.test":   {MailboxPath: pathB},
			}

			orig := []byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: invite\r\n\r\nhello\r\n")
			if err := deliver(accounts, "alice@hermex.test", "bob@hermex.test", pathB, orig, when, int64(mapi.PrivateFIDInbox)); err != nil {
				t.Fatalf("deliver: %v", err)
			}
			if got := len(listInbox(t, pathA)); got != tc.wantReplies {
				t.Errorf("alice inbox = %d, want %d (meeting handled=%v)", got, tc.wantReplies, tc.handled)
			}
		})
	}

	// Panicking meeting hook must not lose delivery, logs failure, and still runs OOF.
	sink := withPassLogger(t)
	prev := OnMeetingRequest
	OnMeetingRequest = func(*objectstore.Store, directory.Accounts, string, string, int64) bool {
		panic("meeting boom")
	}
	t.Cleanup(func() { OnMeetingRequest = prev })
	dir := t.TempDir()
	pathA, pathB := filepath.Join(dir, "alice"), filepath.Join(dir, "bob")
	enableOOF(t, pathB)
	stA, err := objectstore.Open(pathA)
	mustNoErr(t, "open alice", err)
	mustClose(t, stA)
	accounts := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: pathA}, "bob@hermex.test": {MailboxPath: pathB}}
	orig := []byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: invite\r\n\r\nhello\r\n")
	mustNoErr(t, "deliver with panicking meeting hook", deliver(accounts, "alice@hermex.test", "bob@hermex.test", pathB, orig, when, int64(mapi.PrivateFIDInbox)))
	wantEq(t, "OOF reply after meeting panic", len(listInbox(t, pathA)), 1)
	e, ok := sink.find("delivery.pass.fail")
	if !ok || e.Fields["pass"] != "meeting-auto-process" {
		t.Errorf("missing meeting-auto-process failure event: %+v", e)
	}
}
