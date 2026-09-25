package ews

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxews"
)

// hobbies is PidTagHobbies (MS-OXPROPS), a property UpdateItem has no field
// for, standing in for what another client stored.
const hobbies = mapi.PropTag(0x3A43001F)

// TestUpdateItemEditsTheDraftInPlace edits a draft that carries an attachment
// and a foreign property: the message keeps its id and both, takes a new uid and
// change key, lists under the new subject, and leaves nothing recoverable.
func TestUpdateItemEditsTheDraftInPlace(t *testing.T) {
	ts, dir := seededWithMessage(t, draftMessage)
	mid := seedForeignState(t, dir)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	oldID, oldKey := itemKey(t, fi)

	_, out := soapPost(t, ts, updateReq(oldID, "item:Subject", `<t:Subject>Edited</t:Subject>`), true)
	newID, newKey := itemKey(t, out)
	wantEditedInPlace(t, oldID, newID, oldKey, newKey)

	st, msgs := inboxUIDs(t, dir)
	defer st.Close()
	if len(msgs) != 1 || msgs[0].ID != mid || msgs[0].Subject != "Edited" {
		t.Fatalf("inbox = %+v, want message %d listed as Edited", msgs, mid)
	}
	msg, err := st.OpenMessage(mid)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := msg.Props.Get(hobbies); v != "chess" || len(msg.Attachments) != 1 {
		t.Errorf("foreign property %v and %d attachments, want chess and 1", v, len(msg.Attachments))
	}
	deleted, err := st.ListAllSoftDeleted()
	if err != nil || len(deleted) != 0 {
		t.Errorf("recoverable items = %d (%v), want 0", len(deleted), err)
	}
}

// seedForeignState gives the one Inbox message what only another client writes,
// a foreign property and an attachment, and returns its id.
func seedForeignState(t *testing.T, dir string) int64 {
	t.Helper()
	st, msgs := inboxUIDs(t, dir)
	defer st.Close()
	mid := msgs[0].ID
	if err := st.SetMessageProperties(mid, mapi.PropertyValues{{Tag: hobbies, Value: "chess"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateAttachment(mid, mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachLongFilename, Value: "notes.txt"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("notes")},
	}); err != nil {
		t.Fatal(err)
	}
	return mid
}

// wantEditedInPlace holds the response ids to one message under a new uid and a
// new change key.
func wantEditedInPlace(t *testing.T, oldID, newID, oldKey, newKey string) {
	t.Helper()
	before, err := oxews.DecodeItemID(oldID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := oxews.DecodeItemID(newID)
	if err != nil {
		t.Fatal(err)
	}
	if after.MessageID != before.MessageID || after.UID <= before.UID {
		t.Errorf("edited item is message %d uid %d, want message %d under a uid above %d",
			after.MessageID, after.UID, before.MessageID, before.UID)
	}
	if newKey == oldKey || newKey == "" {
		t.Errorf("change key %q after the edit, was %q", newKey, oldKey)
	}
}
