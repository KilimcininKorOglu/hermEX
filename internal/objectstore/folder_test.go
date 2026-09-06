package objectstore

import (
	"bytes"
	"database/sql"
	"fmt"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// TestSeedMailboxFolderTree checks that a fresh mailbox carries the full
// built-in folder hierarchy with correct parentage, container classes, and
// hidden flags.
func TestSeedMailboxFolderTree(t *testing.T) {
	s := openSeededStore(t)

	// Every built-in id (0x01..0x1d) is present: the generic set plus the
	// spooler-queue search folder.
	var n int
	mustScan(t, s.objdb.QueryRow(`SELECT COUNT(*) FROM folders`), &n)
	wantEq(t, "folder count", n, len(builtinFolders)+1)

	wantParentage(t, s)

	// Inbox display name and container class.
	im := folderProps(t, s, mapi.PrivateFIDInbox, mapi.PrDisplayName, mapi.PrContainerClass)
	wantEq(t, "inbox display name", im[mapi.PrDisplayName], any("Inbox"))
	wantEq(t, "inbox container class", im[mapi.PrContainerClass], any(mapi.ContainerClassNote))

	wantHiddenFlags(t, s)

	// The spooler queue is a search folder with no message range.
	var isSearch, curEID, maxEID int64
	mustScan(t, s.objdb.QueryRow(`SELECT is_search, cur_eid, max_eid FROM folders WHERE folder_id=?`, mapi.PrivateFIDSpoolerQueue),
		&isSearch, &curEID, &maxEID)
	wantEq(t, "spooler queue is_search", isSearch, int64(1))
	wantEq(t, "spooler queue cur_eid", curEID, int64(0))
	wantEq(t, "spooler queue max_eid", maxEID, int64(0))
}

// wantParentage checks the root has no parent and a child points at its parent.
func wantParentage(t *testing.T, s *Store) {
	t.Helper()
	var parent sql.NullInt64
	mustScan(t, s.objdb.QueryRow(`SELECT parent_id FROM folders WHERE folder_id=?`, mapi.PrivateFIDRoot), &parent)
	if parent.Valid {
		t.Errorf("root parent = %d, want NULL", parent.Int64)
	}
	mustScan(t, s.objdb.QueryRow(`SELECT parent_id FROM folders WHERE folder_id=?`, mapi.PrivateFIDInbox), &parent)
	if !parent.Valid {
		t.Fatalf("inbox has no parent, want %#x", mapi.PrivateFIDIPMSubtree)
	}
	wantEq(t, "inbox parent", parent.Int64, int64(mapi.PrivateFIDIPMSubtree))
}

// wantHiddenFlags checks hidden folders carry PR_ATTR_HIDDEN and visible ones
// do not.
func wantHiddenFlags(t *testing.T, s *Store) {
	t.Helper()
	hidden := []int64{mapi.PrivateFIDQuickContacts, mapi.PrivateFIDIMContactList, mapi.PrivateFIDGALContacts, mapi.PrivateFIDConversationActionSettings}
	for _, fid := range hidden {
		p := folderProps(t, s, fid, mapi.PrAttrHidden)
		wantEq(t, fmt.Sprintf("folder %#x PR_ATTR_HIDDEN", fid), p[mapi.PrAttrHidden], any(true))
	}
	for _, fid := range []int64{mapi.PrivateFIDInbox, mapi.PrivateFIDSentItems, mapi.PrivateFIDCalendar} {
		p := folderProps(t, s, fid, mapi.PrAttrHidden)
		wantEq(t, fmt.Sprintf("folder %#x PR_ATTR_HIDDEN entries", fid), len(p), 0)
	}
}

// TestSeedMailboxCounters checks the store counters and EID ranges after the
// built-in tree is seeded.
func TestSeedMailboxCounters(t *testing.T) {
	s := openSeededStore(t)

	folders := len(builtinFolders) + 1 // generic folders plus the search folder
	if got := configVal(t, s, cfgLastChangeNumber); got != uint64(folders) {
		t.Errorf("LAST_CHANGE_NUMBER = %d, want %d", got, folders)
	}
	if got := configVal(t, s, cfgLastArticleNumber); got != uint64(folders) {
		t.Errorf("LAST_ARTICLE_NUMBER = %d, want %d", got, folders)
	}
	// The store EID cursor is untouched by folder seeding (folders carve their
	// own ranges); it still points at the start of the custom region.
	if got := configVal(t, s, cfgCurrentEID); got != customEIDBegin {
		t.Errorf("CURRENT_EID = %#x, want %#x", got, customEIDBegin)
	}

	// allocated_eids: the seed low range plus one range per generic folder.
	var ranges int
	if err := s.objdb.QueryRow(`SELECT COUNT(*) FROM allocated_eids`).Scan(&ranges); err != nil {
		t.Fatal(err)
	}
	if want := 1 + len(builtinFolders); ranges != want {
		t.Errorf("allocated_eids rows = %d, want %d", ranges, want)
	}
	// Change numbers are unique and dense over 1..folders.
	var minCN, maxCN, distinct int64
	if err := s.objdb.QueryRow(`SELECT MIN(change_number), MAX(change_number), COUNT(DISTINCT change_number) FROM folders`).Scan(&minCN, &maxCN, &distinct); err != nil {
		t.Fatal(err)
	}
	if minCN != 1 || maxCN != int64(folders) || distinct != int64(folders) {
		t.Errorf("change numbers = [%d,%d] distinct %d, want [1,%d] distinct %d", minCN, maxCN, distinct, folders, folders)
	}
}

// TestSeedMailboxChangeKey verifies the change key and predecessor change list
// stamped on a seeded folder: an XID of the store replica GUID and the change
// number's global counter, and a one-entry PCL holding the same XID.
func TestSeedMailboxChangeKey(t *testing.T) {
	s := openSeededStore(t)

	guid, err := s.storeGUID()
	mustNoErr(t, "store guid", err)
	var cn uint64
	mustScan(t, s.objdb.QueryRow(`SELECT change_number FROM folders WHERE folder_id=?`, mapi.PrivateFIDInbox), &cn)
	gc := mapi.ValueToGC(cn)

	pm := folderProps(t, s, mapi.PrivateFIDInbox, mapi.PrChangeKey, mapi.PrPredecessorChangeList)
	wantChangeKey(t, pm[mapi.PrChangeKey], guid, gc[:])
	wantPCL(t, pm[mapi.PrPredecessorChangeList], guid, gc[:])
}

// wantChangeKey decodes PR_CHANGE_KEY and checks it is the store replica GUID
// plus the change number's global counter.
func wantChangeKey(t *testing.T, v any, guid string, gc []byte) {
	t.Helper()
	blob, ok := v.([]byte)
	if !ok {
		t.Fatalf("change key missing or wrong type: %T", v)
	}
	wantEq(t, "change key length (16-byte GUID + 6-byte global counter)", len(blob), 22)
	xid, err := ext.NewPull(blob, propExtFlags).XID(len(blob))
	mustNoErr(t, "decode change key", err)
	wantEq(t, "change key GUID", xid.GUID.String(), guid)
	if !bytes.Equal(xid.LocalID, gc) {
		t.Errorf("change key local id = %x, want %x", xid.LocalID, gc)
	}
}

// wantPCL decodes PR_PREDECESSOR_CHANGE_LIST and checks it holds the one XID
// the change key carries.
func wantPCL(t *testing.T, v any, guid string, gc []byte) {
	t.Helper()
	blob, ok := v.([]byte)
	if !ok {
		t.Fatalf("PCL missing or wrong type: %T", v)
	}
	wantEq(t, "PCL length (one size byte + the 22-byte XID)", len(blob), 23)
	xids, err := ext.NewPull(blob, propExtFlags).PCL()
	mustNoErr(t, "decode PCL", err)
	if len(xids) != 1 {
		t.Fatalf("PCL holds %d XIDs, want 1", len(xids))
	}
	wantEq(t, "PCL XID GUID", xids[0].GUID.String(), guid)
	if !bytes.Equal(xids[0].LocalID, gc) {
		t.Errorf("PCL XID local id = %x, want %x", xids[0].LocalID, gc)
	}
}

// TestSeedMailboxReceiveAndPermissions checks the receive-folder map and the
// default free/busy permissions.
func TestSeedMailboxReceiveAndPermissions(t *testing.T) {
	s := openSeededStore(t)

	var fid int64
	if err := s.objdb.QueryRow(`SELECT folder_id FROM receive_table WHERE class=''`).Scan(&fid); err != nil {
		t.Fatal(err)
	}
	if fid != mapi.PrivateFIDInbox {
		t.Errorf("default receive folder = %#x, want inbox %#x", fid, mapi.PrivateFIDInbox)
	}
	var rcvCount int
	if err := s.objdb.QueryRow(`SELECT COUNT(*) FROM receive_table`).Scan(&rcvCount); err != nil {
		t.Fatal(err)
	}
	if rcvCount != 4 {
		t.Errorf("receive_table rows = %d, want 4", rcvCount)
	}

	// Pin the seeded bytes to the literal rights, independent of the mapi
	// constants that feed the seed: 0xC00 = FreeBusySimple|Visible, 0x800 =
	// FreeBusySimple. A drift in either constant or the seed fails here.
	var perm int
	if err := s.objdb.QueryRow(`SELECT permission FROM permissions WHERE folder_id=? AND username='default'`, mapi.PrivateFIDCalendar).Scan(&perm); err != nil {
		t.Fatal(err)
	}
	if perm != 0xC00 {
		t.Errorf("calendar default permission = %#x, want 0xC00 (FreeBusySimple|Visible)", perm)
	}
	if err := s.objdb.QueryRow(`SELECT permission FROM permissions WHERE folder_id=? AND username='default'`, mapi.PrivateFIDLocalFreebusy).Scan(&perm); err != nil {
		t.Fatal(err)
	}
	if perm != 0x800 {
		t.Errorf("free/busy default permission = %#x, want 0x800 (FreeBusySimple)", perm)
	}
}
