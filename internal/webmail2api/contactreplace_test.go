package webmail2api

import (
	"errors"
	"strconv"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestAFailedContactReplaceKeepsTheContact changes a contact into a group, the
// edit that replaces the stored object, and makes writing the replacement fail.
// The user's contact must still be there: an edit that could not be saved must
// never cost the data it was editing.
func TestAFailedContactReplaceKeepsTheContact(t *testing.T) {
	dir := freshMailbox(t)
	old := seedObject(t, dir, int64(mapi.PrivateFIDContacts), "IPM.Contact")

	prev := writeContact
	writeContact = func(*objectstore.Store, contactJSON) (int64, error) { return 0, errors.New("disk full") }
	defer func() { writeContact = prev }()

	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer mustClose(t, st)
	id, err := saveContactEdit(st, strconv.FormatInt(old, 10), contactJSON{IsGroup: true, Name: "Team"})
	if err == nil {
		t.Fatal("the failed write was reported as saved")
	}
	wantEq(t, "id reported for a failed edit", id, int64(0))
	msg, err := st.OpenMessage(old)
	if err != nil {
		t.Fatalf("the contact is gone after a failed edit: %v", err)
	}
	wantEq(t, "class after a failed edit", propStr(msg.Props, mapi.PrMessageClass), "IPM.Contact")
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDContacts))
	mustNoErr(t, "list contacts", err)
	wantEq(t, "objects in Contacts after a failed edit", len(objs), 1)
}
