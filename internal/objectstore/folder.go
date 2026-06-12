package objectstore

import (
	"database/sql"
	"fmt"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// Default-user folder rights seeded so free/busy lookups resolve: visibility
// plus simple (availability-only) free/busy access.
const (
	frightsVisible        = 0x400
	frightsFreeBusySimple = 0x800
)

// builtinFolder describes one default folder to seed on a fresh mailbox: its
// fixed id, its parent (0 = no parent, used by the root), the display name, the
// container class ("" = none), and whether it is hidden from clients.
type builtinFolder struct {
	fid       uint64
	parent    uint64
	dispName  string
	contClass string
	hidden    bool
}

// builtinFolders is the default private-store hierarchy. The order is
// significant: change numbers, article numbers, and message-id ranges are
// assigned in creation order, so this list is kept in the canonical creation
// order. Display names are the English defaults; localization is applied by the
// client, not stored. The spooler queue is a search folder created separately.
var builtinFolders = []builtinFolder{
	{mapi.PrivateFIDRoot, 0, "Root Container", "", false},
	{mapi.PrivateFIDIPMSubtree, mapi.PrivateFIDRoot, "Top of Information Store", "", false},
	{mapi.PrivateFIDInbox, mapi.PrivateFIDIPMSubtree, "Inbox", mapi.ContainerClassNote, false},
	{mapi.PrivateFIDDraft, mapi.PrivateFIDIPMSubtree, "Drafts", mapi.ContainerClassNote, false},
	{mapi.PrivateFIDOutbox, mapi.PrivateFIDIPMSubtree, "Outbox", mapi.ContainerClassNote, false},
	{mapi.PrivateFIDSentItems, mapi.PrivateFIDIPMSubtree, "Sent Items", mapi.ContainerClassNote, false},
	{mapi.PrivateFIDDeletedItems, mapi.PrivateFIDIPMSubtree, "Deleted Items", mapi.ContainerClassNote, false},
	{mapi.PrivateFIDContacts, mapi.PrivateFIDIPMSubtree, "Contacts", mapi.ContainerClassContact, false},
	{mapi.PrivateFIDCalendar, mapi.PrivateFIDIPMSubtree, "Calendar", mapi.ContainerClassAppointment, false},
	{mapi.PrivateFIDJournal, mapi.PrivateFIDIPMSubtree, "Journal", mapi.ContainerClassJournal, false},
	{mapi.PrivateFIDNotes, mapi.PrivateFIDIPMSubtree, "Notes", mapi.ContainerClassStickyNote, false},
	{mapi.PrivateFIDTasks, mapi.PrivateFIDIPMSubtree, "Tasks", mapi.ContainerClassTask, false},
	{mapi.PrivateFIDQuickContacts, mapi.PrivateFIDContacts, "Quick Contacts", "IPF.Contact.MOC.QuickContacts", true},
	{mapi.PrivateFIDIMContactList, mapi.PrivateFIDContacts, "IM Contacts List", "IPF.Contact.MOC.ImContactList", true},
	{mapi.PrivateFIDGALContacts, mapi.PrivateFIDContacts, "GAL Contacts", "IPF.Contact.GalContacts", true},
	{mapi.PrivateFIDJunk, mapi.PrivateFIDIPMSubtree, "Junk Email", mapi.ContainerClassNote, false},
	{mapi.PrivateFIDConversationActionSettings, mapi.PrivateFIDIPMSubtree, "Conversation Action Settings", "IPF.Configuration", true},
	{mapi.PrivateFIDDeferredAction, mapi.PrivateFIDRoot, "Deferred Action", "", false},
	{mapi.PrivateFIDCommonViews, mapi.PrivateFIDRoot, "Common Views", "", false},
	{mapi.PrivateFIDSchedule, mapi.PrivateFIDRoot, "Schedule", "", false},
	{mapi.PrivateFIDFinder, mapi.PrivateFIDRoot, "Finder", "", false},
	{mapi.PrivateFIDViews, mapi.PrivateFIDRoot, "Views", "", false},
	{mapi.PrivateFIDShortcuts, mapi.PrivateFIDRoot, "Shortcuts", "", false},
	{mapi.PrivateFIDSyncIssues, mapi.PrivateFIDIPMSubtree, "Sync Issues", mapi.ContainerClassNote, false},
	{mapi.PrivateFIDConflicts, mapi.PrivateFIDSyncIssues, "Conflicts", mapi.ContainerClassNote, false},
	{mapi.PrivateFIDLocalFailures, mapi.PrivateFIDSyncIssues, "Local Failures", mapi.ContainerClassNote, false},
	{mapi.PrivateFIDServerFailures, mapi.PrivateFIDSyncIssues, "Server Failures", mapi.ContainerClassNote, false},
	{mapi.PrivateFIDLocalFreebusy, mapi.PrivateFIDRoot, "Freebusy Data", "", false},
}

// seedMailbox creates the default folder hierarchy, store-root properties,
// receive-folder map, and default free/busy permissions for a fresh mailbox.
// replica is the store's replica GUID, used to stamp each folder's change key
// and predecessor change list. Everything runs in one transaction so a fresh
// mailbox is either fully provisioned or not at all.
func (s *Store) seedMailbox(replica mapi.GUID) error {
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ntNow := mapi.UnixToNTTime(time.Now())
	if err := seedStoreProps(tx, ntNow); err != nil {
		return err
	}
	for _, f := range builtinFolders {
		if err := createGenericFolder(tx, replica, ntNow, f); err != nil {
			return fmt.Errorf("objectstore: seed folder %#x: %w", f.fid, err)
		}
	}
	if err := createSearchFolder(tx, replica, ntNow,
		mapi.PrivateFIDSpoolerQueue, mapi.PrivateFIDRoot, "Spooler Queue"); err != nil {
		return fmt.Errorf("objectstore: seed spooler queue: %w", err)
	}
	if err := seedReceiveTable(tx, ntNow); err != nil {
		return err
	}
	if err := seedDefaultPermissions(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// seedStoreProps writes the store-root property bag: creation time, out-of-office
// state, and the (zeroed) mailbox size counters.
func seedStoreProps(tx *sql.Tx, ntNow uint64) error {
	props := mapi.PropertyValues{
		{Tag: mapi.PrCreationTime, Value: ntNow},
		{Tag: mapi.PrOOFState, Value: false},
		{Tag: mapi.PrMessageSizeExtended, Value: int64(0)},
		{Tag: mapi.PrNormalMessageSizeExtended, Value: int64(0)},
		{Tag: mapi.PrAssocMessageSizeExtended, Value: int64(0)},
	}
	stmt, err := tx.Prepare(`INSERT INTO store_properties (proptag, propval) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range props {
		enc, err := encodeValue(p.Tag.Type(), p.Value)
		if err != nil {
			return fmt.Errorf("objectstore: encode %s: %w", p.Tag, err)
		}
		if _, err := stmt.Exec(int64(uint32(p.Tag)), enc); err != nil {
			return err
		}
	}
	return nil
}

// createGenericFolder carves a message-id range and a change number for a
// content folder, inserts its row, and writes its property bag.
func createGenericFolder(tx *sql.Tx, replica mapi.GUID, ntNow uint64, f builtinFolder) error {
	begin, end, err := allocateRange(tx)
	if err != nil {
		return err
	}
	cn, err := allocateCN(tx)
	if err != nil {
		return err
	}
	var parent any
	if f.parent != 0 {
		parent = int64(f.parent)
	}
	if _, err := tx.Exec(
		`INSERT INTO folders (folder_id, parent_id, change_number, cur_eid, max_eid) VALUES (?, ?, ?, ?, ?)`,
		int64(f.fid), parent, int64(cn), int64(begin), int64(end)); err != nil {
		return err
	}
	props, err := folderPropertyBag(tx, replica, ntNow, cn, f.dispName, f.contClass, true, f.hidden)
	if err != nil {
		return err
	}
	return insertProps(tx, "folder_properties", "folder_id", int64(f.fid), props)
}

// createSearchFolder inserts a search folder, which owns no message-id range
// (its contents are computed) but still takes a change number and property bag.
func createSearchFolder(tx *sql.Tx, replica mapi.GUID, ntNow uint64, fid, parent uint64, dispName string) error {
	cn, err := allocateCN(tx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO folders (folder_id, parent_id, change_number, is_search, cur_eid, max_eid) VALUES (?, ?, ?, 1, 0, 0)`,
		int64(fid), int64(parent), int64(cn)); err != nil {
		return err
	}
	props, err := folderPropertyBag(tx, replica, ntNow, cn, dispName, mapi.ContainerClassNote, false, false)
	if err != nil {
		return err
	}
	return insertProps(tx, "folder_properties", "folder_id", int64(fid), props)
}

// folderPropertyBag builds the property set written for a newly created folder:
// the zeroed hierarchy counters and article number (plus the next-article seed
// for content folders), the display name and comment, the optional container
// class, the four creation/modification timestamps, the change key and
// predecessor change list, and the hidden flag when set.
func folderPropertyBag(tx *sql.Tx, replica mapi.GUID, ntNow, cn uint64, dispName, contClass string, addNext, hidden bool) (mapi.PropertyValues, error) {
	art, err := allocateArticle(tx)
	if err != nil {
		return nil, err
	}
	ck, err := changeKey(replica, cn)
	if err != nil {
		return nil, err
	}
	pcl, err := predecessorChangeList(replica, cn)
	if err != nil {
		return nil, err
	}
	props := mapi.PropertyValues{
		{Tag: mapi.PrDeletedCountTotal, Value: int32(0)},
		{Tag: mapi.PrDeletedFolderCount, Value: int32(0)},
		{Tag: mapi.PrHierarchyChangeNum, Value: int32(0)},
		{Tag: mapi.PrInternetArticleNumber, Value: int32(art)},
	}
	if addNext {
		props = append(props, mapi.TaggedPropVal{Tag: mapi.PrInternetArticleNumberNext, Value: int32(1)})
	}
	props = append(props,
		mapi.TaggedPropVal{Tag: mapi.PrDisplayName, Value: dispName},
		mapi.TaggedPropVal{Tag: mapi.PrComment, Value: ""},
	)
	if contClass != "" {
		props = append(props, mapi.TaggedPropVal{Tag: mapi.PrContainerClass, Value: contClass})
	}
	props = append(props,
		mapi.TaggedPropVal{Tag: mapi.PrCreationTime, Value: ntNow},
		mapi.TaggedPropVal{Tag: mapi.PrLastModificationTime, Value: ntNow},
		mapi.TaggedPropVal{Tag: mapi.PrHierRev, Value: ntNow},
		mapi.TaggedPropVal{Tag: mapi.PrLocalCommitTimeMax, Value: ntNow},
		mapi.TaggedPropVal{Tag: mapi.PrChangeKey, Value: ck},
		mapi.TaggedPropVal{Tag: mapi.PrPredecessorChangeList, Value: pcl},
	)
	if hidden {
		props = append(props, mapi.TaggedPropVal{Tag: mapi.PrAttrHidden, Value: true})
	}
	return props, nil
}

// changeKey serializes the folder's change key: an XID pairing the store replica
// GUID with the 6-byte global counter of the change number.
func changeKey(replica mapi.GUID, cn uint64) ([]byte, error) {
	gc := mapi.ValueToGC(cn)
	p := ext.NewPush(propExtFlags)
	if err := p.XID(mapi.XID{GUID: replica, LocalID: gc[:]}); err != nil {
		return nil, err
	}
	return p.Bytes(), nil
}

// predecessorChangeList serializes a single-entry predecessor change list
// holding the same XID as the change key.
func predecessorChangeList(replica mapi.GUID, cn uint64) ([]byte, error) {
	gc := mapi.ValueToGC(cn)
	p := ext.NewPush(propExtFlags)
	if err := p.PCL([]mapi.XID{{GUID: replica, LocalID: gc[:]}}); err != nil {
		return nil, err
	}
	return p.Bytes(), nil
}

// insertProps writes a property bag into an object's _properties table within a
// transaction. table and idCol are internal constants, never caller input.
func insertProps(tx *sql.Tx, table, idCol string, id int64, props mapi.PropertyValues) error {
	stmt, err := tx.Prepare(fmt.Sprintf(
		`INSERT INTO %s (%s, proptag, propval) VALUES (?, ?, ?)`, table, idCol))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range props {
		enc, err := encodeValue(p.Tag.Type(), p.Value)
		if err != nil {
			return fmt.Errorf("objectstore: encode %s: %w", p.Tag, err)
		}
		if _, err := stmt.Exec(id, int64(uint32(p.Tag)), enc); err != nil {
			return err
		}
	}
	return nil
}

// seedReceiveTable maps message classes to their delivery folders: the empty
// (default) class and IPM mail to the inbox, IPC to the root.
func seedReceiveTable(tx *sql.Tx, ntNow uint64) error {
	rows := []struct {
		class string
		fid   uint64
	}{
		{"", mapi.PrivateFIDInbox},
		{"IPC", mapi.PrivateFIDRoot},
		{"IPM", mapi.PrivateFIDInbox},
		{"REPORT.IPM", mapi.PrivateFIDInbox},
	}
	for _, r := range rows {
		if _, err := tx.Exec(
			`INSERT INTO receive_table (class, folder_id, modified_time) VALUES (?, ?, ?)`,
			r.class, int64(r.fid), int64(ntNow)); err != nil {
			return err
		}
	}
	return nil
}

// seedDefaultPermissions grants the default user simple free/busy access on the
// calendar and the dedicated free/busy folder.
func seedDefaultPermissions(tx *sql.Tx) error {
	rows := []struct {
		fid  uint64
		perm int
	}{
		{mapi.PrivateFIDCalendar, frightsFreeBusySimple | frightsVisible},
		{mapi.PrivateFIDLocalFreebusy, frightsFreeBusySimple},
	}
	for _, r := range rows {
		if _, err := tx.Exec(
			`INSERT INTO permissions (folder_id, username, permission) VALUES (?, 'default', ?)`,
			int64(r.fid), r.perm); err != nil {
			return err
		}
	}
	return nil
}
