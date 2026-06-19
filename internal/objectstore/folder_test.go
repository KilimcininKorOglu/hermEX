package objectstore

import (
	"bytes"
	"database/sql"
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
	if err := s.objdb.QueryRow(`SELECT COUNT(*) FROM folders`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if want := len(builtinFolders) + 1; n != want {
		t.Errorf("folder count = %d, want %d", n, want)
	}

	// The root has no parent; a child points at its parent.
	var parent sql.NullInt64
	if err := s.objdb.QueryRow(`SELECT parent_id FROM folders WHERE folder_id=?`, mapi.PrivateFIDRoot).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent.Valid {
		t.Errorf("root parent = %d, want NULL", parent.Int64)
	}
	if err := s.objdb.QueryRow(`SELECT parent_id FROM folders WHERE folder_id=?`, mapi.PrivateFIDInbox).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if !parent.Valid || parent.Int64 != mapi.PrivateFIDIPMSubtree {
		t.Errorf("inbox parent = %v, want %#x", parent, mapi.PrivateFIDIPMSubtree)
	}

	// Inbox display name and container class.
	inbox, err := s.GetFolderProperties(mapi.PrivateFIDInbox, mapi.PrDisplayName, mapi.PrContainerClass)
	if err != nil {
		t.Fatal(err)
	}
	im := asMap(inbox)
	if im[mapi.PrDisplayName] != "Inbox" {
		t.Errorf("inbox display name = %v, want Inbox", im[mapi.PrDisplayName])
	}
	if im[mapi.PrContainerClass] != mapi.ContainerClassNote {
		t.Errorf("inbox container class = %v, want %s", im[mapi.PrContainerClass], mapi.ContainerClassNote)
	}

	// Hidden folders carry PR_ATTR_HIDDEN; visible ones do not.
	for _, fid := range []int64{mapi.PrivateFIDQuickContacts, mapi.PrivateFIDIMContactList, mapi.PrivateFIDGALContacts, mapi.PrivateFIDConversationActionSettings} {
		p, err := s.GetFolderProperties(fid, mapi.PrAttrHidden)
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 1 || p[0].Value != true {
			t.Errorf("folder %#x: PR_ATTR_HIDDEN = %v, want true", fid, p)
		}
	}
	for _, fid := range []int64{mapi.PrivateFIDInbox, mapi.PrivateFIDSentItems, mapi.PrivateFIDCalendar} {
		p, err := s.GetFolderProperties(fid, mapi.PrAttrHidden)
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 0 {
			t.Errorf("folder %#x: unexpectedly hidden (%v)", fid, p)
		}
	}

	// The spooler queue is a search folder with no message range.
	var isSearch, curEID, maxEID int64
	if err := s.objdb.QueryRow(`SELECT is_search, cur_eid, max_eid FROM folders WHERE folder_id=?`, mapi.PrivateFIDSpoolerQueue).Scan(&isSearch, &curEID, &maxEID); err != nil {
		t.Fatal(err)
	}
	if isSearch != 1 || curEID != 0 || maxEID != 0 {
		t.Errorf("spooler queue: is_search=%d cur_eid=%d max_eid=%d, want 1/0/0", isSearch, curEID, maxEID)
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
	if err != nil {
		t.Fatal(err)
	}
	var cn uint64
	if err := s.objdb.QueryRow(`SELECT change_number FROM folders WHERE folder_id=?`, mapi.PrivateFIDInbox).Scan(&cn); err != nil {
		t.Fatal(err)
	}
	gc := mapi.ValueToGC(cn)

	props, err := s.GetFolderProperties(mapi.PrivateFIDInbox, mapi.PrChangeKey, mapi.PrPredecessorChangeList)
	if err != nil {
		t.Fatal(err)
	}
	pm := asMap(props)

	ckBlob, ok := pm[mapi.PrChangeKey].([]byte)
	if !ok {
		t.Fatalf("change key missing or wrong type: %T", pm[mapi.PrChangeKey])
	}
	if len(ckBlob) != 22 { // 16-byte GUID + 6-byte global counter
		t.Errorf("change key length = %d, want 22", len(ckBlob))
	}
	xid, err := ext.NewPull(ckBlob, propExtFlags).XID(len(ckBlob))
	if err != nil {
		t.Fatal(err)
	}
	if xid.GUID.String() != guid {
		t.Errorf("change key GUID = %s, want %s", xid.GUID.String(), guid)
	}
	if !bytes.Equal(xid.LocalID, gc[:]) {
		t.Errorf("change key local id = %x, want %x", xid.LocalID, gc[:])
	}

	pclBlob, ok := pm[mapi.PrPredecessorChangeList].([]byte)
	if !ok {
		t.Fatalf("PCL missing or wrong type: %T", pm[mapi.PrPredecessorChangeList])
	}
	if len(pclBlob) != 23 { // one size byte + the 22-byte XID
		t.Errorf("PCL length = %d, want 23", len(pclBlob))
	}
	xids, err := ext.NewPull(pclBlob, propExtFlags).PCL()
	if err != nil {
		t.Fatal(err)
	}
	if len(xids) != 1 || xids[0].GUID.String() != guid || !bytes.Equal(xids[0].LocalID, gc[:]) {
		t.Errorf("PCL = %+v, want one XID matching the change key", xids)
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
