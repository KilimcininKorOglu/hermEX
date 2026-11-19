package activesync

import (
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/wbxml"
)

func calendarID() string { return strconv.FormatInt(int64(mapi.PrivateFIDCalendar), 10) }

// userFolder creates a folder of the given FolderCreate Type through the device
// protocol and returns its server id.
func userFolder(t *testing.T, ts *httptest.Server, name, typ string) string {
	t.Helper()
	req := folderCreateReq(primeHierarchy(t, ts), "0", name)
	req.Children[3].Text = typ
	_, root := postCommand(t, ts, "FolderCreate", req)
	wantEq(t, "FolderCreate Status of "+name, root.ChildText(wbxml.FHStatus), "1")
	return root.ChildText(wbxml.FHServerID)
}

// storeObject files an object-store item of the given class into a folder.
func storeObject(t *testing.T, dir string, folder int64, class, subject string) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	props := mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: class},
		{Tag: mapi.PrSubject, Value: subject},
	}
	if class == "IPM.Appointment" {
		// A calendar item a device can be sent needs its start and end.
		ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
			mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole})
		mustNoErr(t, "named ids", err)
		start := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
		props = append(props,
			mapi.TaggedPropVal{Tag: mapi.MakeTag(ids[0], mapi.PtSysTime), Value: mapi.UnixToNTTime(start)},
			mapi.TaggedPropVal{Tag: mapi.MakeTag(ids[1], mapi.PtSysTime), Value: mapi.UnixToNTTime(start.Add(time.Hour))})
	}
	id, err := st.CreateMessage(folder, &oxcmail.Message{Props: props})
	mustNoErr(t, "store "+subject, err)
	return id
}

// liveFolderOf reports the folder holding a live item, or 0 when the store holds
// it in no folder as a live item.
func liveFolderOf(t *testing.T, dir string, id int64) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	// A soft-deleted item keeps its folder, so a lookup error is a failure, not
	// an item that is not live.
	fid, err := st.MessageFolder(id)
	mustNoErr(t, "find the item's folder", err)
	objs, err := st.ListFolderObjects(fid)
	mustNoErr(t, "list the folder", err)
	for _, o := range objs {
		if o.ID == id {
			return fid
		}
	}
	return 0
}

// moveStatus posts one Move and returns its Status and DstMsgId.
func moveStatus(t *testing.T, ts *httptest.Server, srcMsg, srcFld, dstFld string) (string, string) {
	t.Helper()
	_, root := postCommand(t, ts, "MoveItems", moveReq(srcMsg, srcFld, dstFld))
	resp := root.Child(wbxml.MOResponse)
	if resp == nil {
		t.Fatal("MoveItems carried no Response")
	}
	return resp.ChildText(wbxml.MOStatus), resp.ChildText(wbxml.MODstMsgId)
}

// TestMoveItemsMovesACalendarItem proves a calendar item moves between two
// calendar folders with Status 3 ([MS-ASCMD] 2.2.3.177.10), that the store then
// holds it in the destination, and that the DstMsgId names it there.
func TestMoveItemsMovesACalendarItem(t *testing.T) {
	ts, dir := seededServer(t)
	team := userFolder(t, ts, "Team", "13")
	id := storeObject(t, dir, int64(mapi.PrivateFIDCalendar), "IPM.Appointment", "Standup")

	status, dst := moveStatus(t, ts, strconv.FormatInt(id, 10), calendarID(), team)
	wantEq(t, "Status", status, "3")
	dstID, err := strconv.ParseInt(dst, 10, 64)
	mustNoErr(t, "parse DstMsgId", err)
	teamID, _ := strconv.ParseInt(team, 10, 64)
	wantEq(t, "the folder holding the DstMsgId", liveFolderOf(t, dir, dstID), teamID)
}

// TestMoveItemsShowsTheMoveToOtherDevices proves the MS-ASCMD 2.2.1.12 rule that
// a moved item reaches a device that synced the source folder as a Delete there,
// so a second device does not keep a copy of an item that left.
func TestMoveItemsShowsTheMoveToOtherDevices(t *testing.T) {
	ts, dir := seededServer(t)
	team := userFolder(t, ts, "Team", "13")
	id := storeObject(t, dir, int64(mapi.PrivateFIDCalendar), "IPM.Appointment", "Standup")
	sid := strconv.FormatInt(id, 10)

	key := calendarSyncKey(t, ts)
	_, root := postCommand(t, ts, "Sync", calendarSyncReq(key))
	key = respColl(t, root).ChildText(wbxml.ASSyncKey)

	status, _ := moveStatus(t, ts, sid, calendarID(), team)
	wantEq(t, "Status", status, "3")
	_, root = postCommand(t, ts, "Sync", calendarSyncReq(key))
	cmds := respColl(t, root).Child(wbxml.ASCommands)
	if cmds == nil {
		t.Fatal("the source folder reported no change after the move")
	}
	for _, c := range cmds.Children {
		if c.Tag == wbxml.ASDelete && c.ChildText(wbxml.ASServerID) == sid {
			return
		}
	}
	t.Errorf("the source folder did not report the moved item %s as deleted", sid)
}

// calendarSyncReq builds a single-collection Sync request for the Calendar.
func calendarSyncReq(key string) *wbxml.Node {
	return wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections, wbxml.Elem(wbxml.ASCollection,
		wbxml.Str(wbxml.ASSyncKey, key),
		wbxml.Str(wbxml.ASCollectionID, calendarID()))))
}

// calendarSyncKey primes a Calendar sync and returns the first sync key.
func calendarSyncKey(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, root := postCommand(t, ts, "Sync", calendarSyncReq("0"))
	return respColl(t, root).ChildText(wbxml.ASSyncKey)
}

// TestMoveItemsRefusesAnObjectItHasNoClaimOn proves an object id the source
// folder does not hold live is Status 1 ("invalid source Item ID", [MS-ASCMD]
// 2.2.3.177.10) and that nothing moves: an item of another folder named under
// this one, a soft-deleted item (which stays in the dumpster), and ids that are
// not a store id at all.
func TestMoveItemsRefusesAnObjectItHasNoClaimOn(t *testing.T) {
	ts, dir := seededServer(t)
	team := userFolder(t, ts, "Team", "13")
	contact := storeObject(t, dir, int64(mapi.PrivateFIDContacts), "IPM.Contact", "Ann")
	deleted := storeObject(t, dir, int64(mapi.PrivateFIDCalendar), "IPM.Appointment", "Gone")
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	mustNoErr(t, "soft-delete", st.SoftDeleteObject(deleted))
	st.Close()

	for _, src := range []string{strconv.FormatInt(contact, 10), strconv.FormatInt(deleted, 10),
		"99999999999999999999", "-5", "0", "abc"} {
		status, dst := moveStatus(t, ts, src, calendarID(), team)
		wantEq(t, "Status for source "+src, status, "1")
		wantEq(t, "DstMsgId for source "+src, dst, src)
	}
	wantEq(t, "the folder of the contact", liveFolderOf(t, dir, contact), int64(mapi.PrivateFIDContacts))
	wantEq(t, "the deleted item is still not live", liveFolderOf(t, dir, deleted), int64(0))
}

// TestMoveItemsKeepsAnItemInFoldersOfItsKind proves a destination that holds
// another kind of item is refused with Status 2 and nothing moves: a contact
// into a calendar folder, a calendar item into the Inbox, and mail into the
// Calendar. No surface of such a folder could read the item.
func TestMoveItemsKeepsAnItemInFoldersOfItsKind(t *testing.T) {
	ts, dir := seededServer(t)
	team := userFolder(t, ts, "Team", "13")
	contact := storeObject(t, dir, int64(mapi.PrivateFIDContacts), "IPM.Contact", "Ann")
	appt := storeObject(t, dir, int64(mapi.PrivateFIDCalendar), "IPM.Appointment", "Standup")
	seedInbox(t, dir, 1)
	mail := firstInboxServerID(t, ts)

	for _, tc := range []struct{ name, src, srcFld, dstFld string }{
		{"a contact into a calendar folder", strconv.FormatInt(contact, 10), contactsID(), team},
		{"a calendar item into the Inbox", strconv.FormatInt(appt, 10), calendarID(), inboxID()},
		{"mail into the Calendar", mail, inboxID(), calendarID()},
	} {
		status, dst := moveStatus(t, ts, tc.src, tc.srcFld, tc.dstFld)
		wantEq(t, tc.name+": Status", status, "2")
		wantEq(t, tc.name+": DstMsgId", dst, tc.src)
	}
	wantEq(t, "the folder of the contact", liveFolderOf(t, dir, contact), int64(mapi.PrivateFIDContacts))
	wantEq(t, "the folder of the calendar item", liveFolderOf(t, dir, appt), int64(mapi.PrivateFIDCalendar))
	wantEq(t, "the mail still in the Inbox", firstInboxServerID(t, ts), mail)
}
