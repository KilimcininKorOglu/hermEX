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
	dir := provisionedDir(t)
	sess := NewSession(dir, nil, "owner@hermex.test", WithLogger(logging.New(sink)))
	t.Cleanup(sess.Close)

	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	folderH := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDCalendar)))), ropOpenFolder, "OpenFolder(Calendar)", logonH, 0xFFFFFFFF)[1]

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
// the delegate, the mailbox they reached into, and the right they lacked.
func TestDelegateDenialIsLogged(t *testing.T) {
	sink := &ropSink{}
	callerDir := provisionedDir(t)
	targetDir := provisionedDir(t)
	grantFolderPermission(t, targetDir, int64(mapi.PrivateFIDInbox), "delegate@hermex.test", mapi.RightsReviewer)
	accounts := directory.StaticAccounts{
		"boss@hermex.test":     {MailboxPath: targetDir},
		"delegate@hermex.test": {MailboxPath: callerDir},
	}
	sess := NewSession(callerDir, accounts, "delegate@hermex.test", WithLogger(logging.New(sink)))
	t.Cleanup(sess.Close)

	logonH := dispatchOK(t, sess, delegateLogonRequest(0, 0x01, userDNFor("boss@hermex.test")), ropLogon, "Delegate Logon", 0xFFFFFFFF)[0]
	resp, handles := sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDSentItems)))), []uint32{logonH, 0xFFFFFFFF})
	if ec := ropResultEC(t, resp); ec != ecAccessDenied {
		t.Fatalf("delegate Sent Items open ec = %#x, want AccessDenied; the test needs a real refusal", ec)
	}
	if len(handles) != 2 || handles[1] != 0xFFFFFFFF {
		t.Fatalf("refused OpenFolder handles = %#x, want slot 1 unset", handles)
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
// everywhere, so a normal owner browse must leave no denial behind.
func TestOwnerAccessIsNotLoggedAsDenial(t *testing.T) {
	sink := &ropSink{}
	dir := provisionedDir(t)
	sess := NewSession(dir, nil, "owner@hermex.test", WithLogger(logging.New(sink)))
	t.Cleanup(sess.Close)

	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	handles := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDCalendar)))), ropOpenFolder, "Owner OpenFolder(Calendar)", logonH, 0xFFFFFFFF)
	if len(handles) != 2 || handles[1] == 0xFFFFFFFF {
		t.Fatalf("owner Calendar open handles = %#x, want slot 1 set", handles)
	}
	if n := sink.count("authz.deny"); n != 0 {
		t.Errorf("an owner browse logged %d denial events, want 0", n)
	}
}
