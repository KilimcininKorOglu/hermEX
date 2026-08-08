package ews

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// seedInto appends one message to a mailbox folder and returns its object-store
// message id, which is what an item id carries and what a client can enumerate.
func seedInto(t *testing.T, path string, fid int64, raw string) int64 {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AppendMessage(fid, []byte(raw), time.Unix(1718200000, 0), 0); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.ListFolderObjects(fid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatalf("nothing was stored in folder %d", fid)
	}
	return msgs[len(msgs)-1].ID
}

// forgedID names bob's secret message while claiming the one folder alice may read.
func forgedID(t *testing.T, grantedFID, secretMID int64) string {
	t.Helper()
	return oxews.EncodeItemID(oxews.ItemID{
		FolderID:  grantedFID,
		MessageID: secretMID,
		UID:       1,
		Mailbox:   "bob@hermex.test",
	})
}

// TestGetItemRefusesMessageOutsideTheClaimedFolder proves an item id's two
// client-supplied fields are cross-checked. A delegate granted read on one folder
// used to be able to keep FolderID on that folder and walk MessageID through the
// whole mailbox, because the rights check and the fetch read different fields.
func TestGetItemRefusesMessageOutsideTheClaimedFolder(t *testing.T) {
	ts, paths := delegateServer(t)
	// Alice may read bob's calendar, nothing else.
	grantFolder(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDCalendar), testUser, mapi.RightsReviewer)
	secret := seedInto(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox),
		"From: boss@hermex.test\r\nSubject: confidential-payroll\r\n\r\nsalaries\r\n")

	_, out := soapPost(t, ts, getItemReq(forgedID(t, int64(mapi.PrivateFIDCalendar), secret)), true)

	if strings.Contains(out, "confidential-payroll") {
		t.Errorf("a calendar grant exposed an inbox message:\n%s", out)
	}
	if strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("the forged item id was accepted:\n%s", out)
	}
}

// TestCreateAttachmentRefusesMessageOutsideTheClaimedFolder is the write half: the
// same forged id must not let a delegate attach content to a message in a folder
// they hold no rights on.
func TestCreateAttachmentRefusesMessageOutsideTheClaimedFolder(t *testing.T) {
	ts, paths := delegateServer(t)
	grantFolder(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDCalendar), testUser, mapi.RightsEditor)
	secret := seedInto(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox),
		"From: boss@hermex.test\r\nSubject: confidential\r\n\r\nbody\r\n")

	id := forgedID(t, int64(mapi.PrivateFIDCalendar), secret)
	_, out := soapPost(t, ts, createAttachmentReq(id, "note.txt", "text/plain", "aGk="), true)

	if strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("the forged item id accepted an attachment write:\n%s", out)
	}
}

// TestGetItemStillServesTheGrantedFolder keeps ordinary delegate access working:
// an id whose folder really is the message's parent is served as before.
func TestGetItemStillServesTheGrantedFolder(t *testing.T) {
	ts, paths := delegateServer(t)
	grantFolder(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox), testUser, mapi.RightsReviewer)
	mid := seedInto(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox),
		"From: boss@hermex.test\r\nSubject: shared-with-alice\r\n\r\nbody\r\n")

	id := oxews.EncodeItemID(oxews.ItemID{
		FolderID:  int64(mapi.PrivateFIDInbox),
		MessageID: mid,
		UID:       1,
		Mailbox:   "bob@hermex.test",
	})
	_, out := soapPost(t, ts, getItemReq(id), true)

	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("a legitimate delegate read was refused:\n%s", out)
	}
}
