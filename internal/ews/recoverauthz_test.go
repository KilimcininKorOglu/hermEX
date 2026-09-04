package ews

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// softDeleteIntoDumpster files a message into a mailbox's Inbox and soft-deletes
// it, returning the dumpster row's message id. The row keeps Inbox as its real
// parent, which is the folder any recovery has to be authorized against.
func softDeleteIntoDumpster(t *testing.T, path string) int64 {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	raw := []byte("From: someone@example.org\r\nSubject: private\r\n\r\nowner only\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SoftDeleteMessage(int64(mapi.PrivateFIDInbox), info.UID); err != nil {
		t.Fatal(err)
	}
	dump, err := st.ListAllSoftDeleted()
	if err != nil || len(dump) != 1 {
		t.Fatalf("dumpster = %d items err=%v, want 1", len(dump), err)
	}
	return dump[0].Info.ID
}

// TestMoveItemRecoveryRefusesForeignParent is the dumpster IDOR. The move path
// authorized a delegate against the folder id carried in the client-supplied item
// id, then recovered the message by message id alone, so the folder that was
// checked and the message that was acted on were unrelated. A delegate holding
// rights on one folder could therefore resurrect a message deleted from a folder
// they cannot reach, into a folder they can read.
func TestMoveItemRecoveryRefusesForeignParent(t *testing.T) {
	ts, paths := delegateServer(t)
	bob := paths["bob@hermex.test"]
	msgID := softDeleteIntoDumpster(t, bob)

	// The caller may delete from and create in bob's Drafts, and holds nothing at
	// all on bob's Inbox, where the soft-deleted message actually lived.
	grantFolder(t, bob, int64(mapi.PrivateFIDDraft), testUser,
		mapi.FrightsDeleteAny|mapi.FrightsCreate|mapi.FrightsReadAny)

	// Pair the folder the caller is allowed to use with a message from the folder
	// they are not.
	forged := oxews.EncodeItemID(oxews.ItemID{
		FolderID:  int64(mapi.PrivateFIDDraft),
		MessageID: msgID,
		Mailbox:   "bob@hermex.test",
	})

	_, out := soapPost(t, ts, moveItemReq(forged, "drafts", "bob@hermex.test"), true)
	if strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("recovery across a folder the caller cannot reach succeeded: %s", out)
	}

	st, err := objectstore.Open(bob)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if dump, _ := st.ListAllSoftDeleted(); len(dump) != 1 {
		t.Errorf("dumpster = %d items, want the message left untouched", len(dump))
	}
	if drafts, _ := st.ListMessages(int64(mapi.PrivateFIDDraft)); len(drafts) != 0 {
		t.Errorf("drafts = %d items, want the message never restored there", len(drafts))
	}
}
