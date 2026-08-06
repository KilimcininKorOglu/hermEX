package rop

import (
	"sync"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
)

// ropSink records every event a session emits, so a test can assert on the audit
// trail a ROP batch leaves behind.
type ropSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (c *ropSink) Write(e logging.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

// find returns the first recorded event with the given name.
func (c *ropSink) find(name string) (logging.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// count reports how many events with the given name were recorded.
func (c *ropSink) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.Name == name {
			n++
		}
	}
	return n
}

// TestPermissionChangeIsLogged proves an applied folder ACL change lands in the
// central log. A permission grant made from Outlook is otherwise invisible: the
// transport records one line per POST and a POST carries a whole ROP batch, so
// without this event nothing says who granted access to which folder.
func TestPermissionChangeIsLogged(t *testing.T) {
	sink := &ropSink{}
	dir := t.TempDir()
	sess := NewSession(dir, nil, "owner@hermex.test", WithLogger(logging.New(sink)))
	t.Cleanup(sess.Close)

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDCalendar)))), []uint32{h[0], 0xFFFFFFFF})
	folderH := h[1]

	applyModify(t, sess, folderH, 0, []permDataRow{{
		flags: permRowAdd,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrEntryID, Value: abMemberEntryID("delegate@hermex.test")},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsReviewer)},
		},
	}})

	e, ok := sink.find("permission.modify")
	if !ok {
		t.Fatal("no permission.modify event after an applied ACL change")
	}
	if e.Subsystem != logging.MAPI {
		t.Errorf("subsystem = %q, want mapi", e.Subsystem)
	}
	if e.User != "owner@hermex.test" {
		t.Errorf("user = %q, want the caller who changed the ACL", e.User)
	}
	if e.Fields["mailbox"] != dir {
		t.Errorf("mailbox = %v, want the changed mailbox %q", e.Fields["mailbox"], dir)
	}
	if e.Fields["folder"] != int64(mapi.PrivateFIDCalendar) {
		t.Errorf("folder = %v, want the Calendar fid", e.Fields["folder"])
	}
	if e.Fields["added"] != 1 {
		t.Errorf("added = %v, want 1", e.Fields["added"])
	}
}

// TestDelegateDenialIsLogged proves a refused delegate access is recorded, naming
// the delegate, the mailbox they reached into, and the right they lacked. The
// mailbox must be the target's, not the caller's own: a delegate denial is exactly
// the case where the two differ, so logging the session path would name the wrong
// mailbox in the only situation that matters.
func TestDelegateDenialIsLogged(t *testing.T) {
	sink := &ropSink{}
	callerDir := t.TempDir()
	targetDir := t.TempDir()
	// A grant on the Inbox alone: it opens the store, and leaves Sent Items with no
	// rights at all, so opening Sent Items is refused. (The Calendar would not do:
	// it is seeded with a default free/busy grant that already confers Visible.)
	grantFolderPermission(t, targetDir, int64(mapi.PrivateFIDInbox), "delegate@hermex.test", mapi.RightsReviewer)
	accounts := directory.StaticAccounts{
		"boss@hermex.test":     {MailboxPath: targetDir},
		"delegate@hermex.test": {MailboxPath: callerDir},
	}
	sess := NewSession(callerDir, accounts, "delegate@hermex.test", WithLogger(logging.New(sink)))
	t.Cleanup(sess.Close)

	_, h := sess.Dispatch(delegateLogonRequest(0, 0x01, userDNFor("boss@hermex.test")), []uint32{0xFFFFFFFF})
	resp, _ := sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDSentItems)))), []uint32{h[0], 0xFFFFFFFF})
	if ec := ropResultEC(t, resp); ec != ecAccessDenied {
		t.Fatalf("delegate Sent Items open ec = %#x, want AccessDenied; the test needs a real refusal", ec)
	}

	e, ok := sink.find("authz.deny")
	if !ok {
		t.Fatal("no authz.deny event after a refused delegate access")
	}
	if e.Level != logging.LevelWarn {
		t.Errorf("level = %q, want warn", e.Level)
	}
	if e.User != "delegate@hermex.test" {
		t.Errorf("user = %q, want the refused delegate", e.User)
	}
	if e.Fields["mailbox"] != targetDir {
		t.Errorf("mailbox = %v, want the target mailbox %q, not the delegate's own", e.Fields["mailbox"], targetDir)
	}
	if e.Fields["folder"] != int64(mapi.PrivateFIDSentItems) {
		t.Errorf("folder = %v, want the refused Sent Items fid", e.Fields["folder"])
	}
	if e.Fields["required_rights"] != uint32(mapi.FrightsVisible) {
		t.Errorf("required_rights = %v, want Visible", e.Fields["required_rights"])
	}
}

// TestOwnerAccessIsNotLoggedAsDenial is the control: an owner session is authorized
// everywhere, so a normal owner browse must leave no denial behind. Without this a
// denial event could be emitted on every access and the assertion above would pass
// while the log said nothing useful.
func TestOwnerAccessIsNotLoggedAsDenial(t *testing.T) {
	sink := &ropSink{}
	dir := t.TempDir()
	sess := NewSession(dir, nil, "owner@hermex.test", WithLogger(logging.New(sink)))
	t.Cleanup(sess.Close)

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	resp, _ := sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDCalendar)))), []uint32{h[0], 0xFFFFFFFF})
	if ec := ropResultEC(t, resp); ec != ecSuccess {
		t.Fatalf("owner Calendar open ec = %#x, want success", ec)
	}
	if n := sink.count("authz.deny"); n != 0 {
		t.Errorf("an owner browse logged %d denial events, want 0", n)
	}
}
