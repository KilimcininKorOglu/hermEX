package objectstore

import (
	"errors"
	"os"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// folderByName indexes a ListFolders result by display name.
func folderByName(t *testing.T, s *Store) map[string]FolderInfo {
	t.Helper()
	list, err := s.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]FolderInfo{}
	for _, f := range list {
		m[f.DisplayName] = f
	}
	return m
}

// TestListFoldersScope verifies ListFolders exposes exactly the user-visible
// IPM subtree: standard folders under their real names with a nil ParentID at
// the top level, deeper folders keeping their real parent, and every hidden,
// system, or root container excluded. All folders are subscribed by default.
func TestListFoldersScope(t *testing.T) {
	s := openSeededStore(t)
	folders := folderByName(t, s)

	// Standard mail folders are present under their real built-in names and
	// sit at the top level (parent is the IPM subtree -> reported as nil).
	for _, name := range []string{
		"Inbox", "Drafts", "Outbox", "Sent Items", "Deleted Items", "Junk Email",
		"Contacts", "Calendar", "Tasks", "Notes", "Journal", "Sync Issues",
	} {
		f, ok := folders[name]
		if !ok {
			t.Errorf("ListFolders missing %q", name)
			continue
		}
		if f.ParentID != nil {
			t.Errorf("%q ParentID = %v, want nil (top level)", name, *f.ParentID)
		}
		if !f.Subscribed {
			t.Errorf("%q Subscribed = false, want true by default", name)
		}
	}

	// Inbox carries its fixed built-in id.
	if f := folders["Inbox"]; f.ID != int64(mapi.PrivateFIDInbox) {
		t.Errorf("Inbox id = %d, want %d", f.ID, mapi.PrivateFIDInbox)
	}

	// A deeper folder keeps its real parent rather than collapsing to nil.
	if c, ok := folders["Conflicts"]; !ok {
		t.Error("ListFolders missing Conflicts")
	} else if c.ParentID == nil || *c.ParentID != int64(mapi.PrivateFIDSyncIssues) {
		t.Errorf("Conflicts ParentID = %v, want %d", c.ParentID, mapi.PrivateFIDSyncIssues)
	}

	// Hidden folders and the root container's system folders are never listed,
	// nor is the IPM subtree itself.
	for _, name := range []string{
		"Quick Contacts", "IM Contacts List", "GAL Contacts", "Conversation Action Settings",
		"Root Container", "Top of Information Store", "Views", "Finder", "Schedule",
		"Spooler Queue", "Common Views", "Shortcuts", "Deferred Action", "Freebusy Data",
	} {
		if _, ok := folders[name]; ok {
			t.Errorf("ListFolders unexpectedly exposes %q", name)
		}
	}
}

// TestCreateAndFindFolder creates a top-level user folder and a nested one,
// then resolves both by name and confirms their placement in the tree.
func TestCreateAndFindFolder(t *testing.T) {
	s := openSeededStore(t)

	top, err := s.CreateFolder(nil, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	// A user folder id is allocated above the built-in range.
	if top < int64(mapi.PrivateFIDUnassignedStart) {
		t.Errorf("user folder id = %d, want >= %d", top, mapi.PrivateFIDUnassignedStart)
	}

	id, ok, err := s.FolderByName(nil, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || id != top {
		t.Fatalf("FolderByName(Projects) = (%d,%v), want (%d,true)", id, ok, top)
	}
	if _, ok, _ := s.FolderByName(nil, "Nope"); ok {
		t.Error("FolderByName(Nope) reported found")
	}

	// A nested folder reports its real parent.
	sub, err := s.CreateFolder(&top, "2026")
	if err != nil {
		t.Fatal(err)
	}
	folders := folderByName(t, s)
	if f, ok := folders["Projects"]; !ok || f.ParentID != nil {
		t.Errorf("Projects = %+v, want present with nil parent", f)
	}
	if f, ok := folders["2026"]; !ok || f.ParentID == nil || *f.ParentID != top {
		t.Errorf("2026 = %+v, want parent %d", f, top)
	}
	if id, ok, _ := s.FolderByName(&top, "2026"); !ok || id != sub {
		t.Errorf("FolderByName(under Projects, 2026) = (%d,%v), want (%d,true)", id, ok, sub)
	}
}

// TestSiblingFoldersShareName is the regression for the dropped index name
// uniqueness: two folders with the same display name under different parents
// can each accept (and index) a delivered message.
func TestSiblingFoldersShareName(t *testing.T) {
	s := openSeededStore(t)

	inbox := int64(mapi.PrivateFIDInbox)
	sent := int64(mapi.PrivateFIDSentItems)
	a, err := s.CreateFolder(&inbox, "Reports")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateFolder(&sent, "Reports")
	if err != nil {
		t.Fatal(err)
	}

	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: r\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbody.\r\n")
	if _, err := s.AppendMessage(a, raw, time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append to first Reports: %v", err)
	}
	if _, err := s.AppendMessage(b, raw, time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append to second Reports (name collision regression): %v", err)
	}

	for _, fid := range []int64{a, b} {
		list, err := s.ListMessages(fid)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 {
			t.Errorf("folder %d has %d messages, want 1", fid, len(list))
		}
	}
}

// TestRenameFolder renames a user folder and confirms both lookups and the
// listing reflect the new name.
func TestRenameFolder(t *testing.T) {
	s := openSeededStore(t)

	id, err := s.CreateFolder(nil, "Old")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RenameFolder(id, nil, "New"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.FolderByName(nil, "Old"); ok {
		t.Error("old name still resolves after rename")
	}
	got, ok, err := s.FolderByName(nil, "New")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != id {
		t.Errorf("FolderByName(New) = (%d,%v), want (%d,true)", got, ok, id)
	}
	if _, ok := folderByName(t, s)["New"]; !ok {
		t.Error("renamed folder absent from listing")
	}
	if err := s.RenameFolder(99999, nil, "X"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RenameFolder(missing) = %v, want ErrNotFound", err)
	}
}

// TestSetFolderName confirms an in-place rename keeps the parent, updates the
// name, refuses a live-sibling collision, allows renaming to the folder's own
// name, and reports ErrNotFound for a missing folder.
func TestSetFolderName(t *testing.T) {
	s := openSeededStore(t)
	parent, err := s.CreateFolder(nil, "P")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateFolder(&parent, "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFolder(&parent, "B"); err != nil {
		t.Fatal(err)
	}

	if err := s.SetFolderName(a, "A2"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.FolderByName(&parent, "A2"); !ok {
		t.Error("renamed folder not found under its original parent (name or parent lost)")
	}
	if _, ok, _ := s.FolderByName(&parent, "A"); ok {
		t.Error("old name still resolves after rename")
	}
	if err := s.SetFolderName(a, "B"); !errors.Is(err, ErrFolderExists) {
		t.Errorf("rename onto a sibling = %v, want ErrFolderExists", err)
	}
	if err := s.SetFolderName(a, "A2"); err != nil {
		t.Errorf("rename to own current name = %v, want nil", err)
	}
	if err := s.SetFolderName(99999, "X"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetFolderName(missing) = %v, want ErrNotFound", err)
	}
}

// TestRenameFolderRejectsCycle confirms reparenting a folder into itself or one
// of its descendants is refused (it would make the folder its own ancestor),
// while a legitimate reparent under a non-ancestor still succeeds.
func TestRenameFolderRejectsCycle(t *testing.T) {
	s := openSeededStore(t)
	parent, err := s.CreateFolder(nil, "Parent")
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateFolder(&parent, "Child")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RenameFolder(parent, &child, "Parent"); !errors.Is(err, ErrFolderCycle) {
		t.Errorf("move into descendant = %v, want ErrFolderCycle", err)
	}
	if err := s.RenameFolder(parent, &parent, "Parent"); !errors.Is(err, ErrFolderCycle) {
		t.Errorf("move into self = %v, want ErrFolderCycle", err)
	}
	other, err := s.CreateFolder(nil, "Other")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RenameFolder(other, &parent, "Other"); err != nil {
		t.Errorf("legitimate reparent under a non-ancestor = %v, want nil", err)
	}
}

// TestSetSubscribed toggles subscription on a built-in folder (no index row
// yet) and confirms the listing reflects it, including resubscription.
func TestSetSubscribed(t *testing.T) {
	s := openSeededStore(t)

	if err := s.SetSubscribed(int64(mapi.PrivateFIDJunk), false); err != nil {
		t.Fatal(err)
	}
	if folderByName(t, s)["Junk Email"].Subscribed {
		t.Error("Junk Email still subscribed after unsubscribe")
	}
	if err := s.SetSubscribed(int64(mapi.PrivateFIDJunk), true); err != nil {
		t.Fatal(err)
	}
	if !folderByName(t, s)["Junk Email"].Subscribed {
		t.Error("Junk Email not subscribed after resubscribe")
	}
	if err := s.SetSubscribed(99999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetSubscribed(missing) = %v, want ErrNotFound", err)
	}
}

// TestDeleteFolder removes a user folder holding a message. The folder and its
// index rows go; the MESSAGE does not, because a folder delete routes its mail
// to Recoverable Items like every other delete path, and the cached eml has to
// survive with it or the recovered message cannot be served.
func TestDeleteFolder(t *testing.T) {
	s := openSeededStore(t)

	id, err := s.CreateFolder(nil, "Temp")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: t\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbody.\r\n")
	info, err := s.AppendMessage(id, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	eml := s.emlPath(midString(uint64(info.ID)))
	if _, err := os.Stat(eml); err != nil {
		t.Fatalf("eml missing before delete: %v", err)
	}

	if err := s.DeleteFolder(id); err != nil {
		t.Fatal(err)
	}

	if countRows(t, s, `SELECT COUNT(*) FROM folders WHERE folder_id=?`, id) != 0 {
		t.Error("object folder survived delete")
	}
	assertRecoverable(t, s, info.ID)
	var idxFolders int
	if err := s.idxdb.QueryRow(`SELECT COUNT(*) FROM folders WHERE folder_id=?`, id).Scan(&idxFolders); err != nil {
		t.Fatal(err)
	}
	if idxFolders != 0 {
		t.Error("index folder row survived delete")
	}
	if _, err := os.Stat(eml); err != nil {
		t.Errorf("the recoverable message lost its cached eml: %v", err)
	}
	if _, ok := folderByName(t, s)["Temp"]; ok {
		t.Error("deleted folder still listed")
	}
	if err := s.DeleteFolder(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("repeat delete = %v, want ErrNotFound", err)
	}
}

// assertRecoverable proves a message the folder held is in Recoverable Items
// rather than destroyed: soft-deleted, and filed under Deleted Items so it
// survives the folder row that used to hold it.
func assertRecoverable(t *testing.T, s *Store, messageID int64) {
	t.Helper()
	if countRows(t, s, `SELECT COUNT(*) FROM messages WHERE message_id=? AND is_deleted=1`, messageID) != 1 {
		t.Error("the folder's message was destroyed rather than moved to Recoverable Items")
	}
	if countRows(t, s, `SELECT COUNT(*) FROM messages WHERE message_id=? AND parent_fid=?`,
		messageID, int64(mapi.PrivateFIDDeletedItems)) != 1 {
		t.Error("the recoverable message was not filed under Deleted Items")
	}
}

// TestBuiltinFolderCannotBeRemovedOrRenamed holds the invariant for every
// protocol, not just the one that exposed it: the store provisions these folders
// and every surface addresses them by PrivateFID_* constant, so removing one
// takes its messages with it and renaming one leaves those constants pointing at
// something the user no longer recognises.
func TestBuiltinFolderCannotBeRemovedOrRenamed(t *testing.T) {
	s := openSeededStore(t)

	for _, fid := range []int64{
		int64(mapi.PrivateFIDInbox),
		int64(mapi.PrivateFIDDeletedItems),
		int64(mapi.PrivateFIDCalendar),
		int64(mapi.PrivateFIDIPMSubtree),
	} {
		if err := s.DeleteFolder(fid); !errors.Is(err, ErrBuiltinFolder) {
			t.Errorf("DeleteFolder(%d) = %v, want ErrBuiltinFolder", fid, err)
		}
		if err := s.RenameFolder(fid, nil, "Renamed"); !errors.Is(err, ErrBuiltinFolder) {
			t.Errorf("RenameFolder(%d) = %v, want ErrBuiltinFolder", fid, err)
		}
	}
	// A user folder is still the caller's to remove and rename.
	id, err := s.CreateFolder(nil, "Mine")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RenameFolder(id, nil, "Mine 2"); err != nil {
		t.Errorf("RenameFolder on a user folder: %v", err)
	}
	if err := s.DeleteFolder(id); err != nil {
		t.Errorf("DeleteFolder on a user folder: %v", err)
	}
}
