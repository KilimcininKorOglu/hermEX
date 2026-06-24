package ews

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestFindItemRecoverableItemsDeletions proves the recoverableitemsdeletions
// distinguished folder serves the mailbox-wide Recoverable Items dumpster: a
// soft-deleted message leaves the live folder's FindItem yet appears in the
// recoverable view. The reference does not back this distinguished name; hermEX adds
// it as a modern-Exchange extension.
func TestFindItemRecoverableItemsDeletions(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)

	// Soft-delete the seeded inbox message into the dumpster.
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if len(inbox) != 1 {
		st.Close()
		t.Fatalf("seed inbox = %d, want 1", len(inbox))
	}
	if err := st.SoftDeleteMessage(int64(mapi.PrivateFIDInbox), inbox[0].UID); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()

	// The live inbox no longer lists it.
	_, outLive := soapPost(t, ts, findItemReq("inbox"), true)
	if strings.Contains(outLive, "Hello EWS") {
		t.Errorf("soft-deleted item still listed in the live inbox FindItem: %s", outLive)
	}

	// The Recoverable Items dumpster does.
	resp, out := soapPost(t, ts, findItemReq("recoverableitemsdeletions"), true)
	if resp.StatusCode != 200 {
		t.Fatalf("FindItem recoverable status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("recoverable FindItem not success: %s", out)
	}
	if !strings.Contains(out, "Hello EWS") {
		t.Errorf("recoverable FindItem missing the soft-deleted subject: %s", out)
	}
}

// TestMoveItemRecoverFromDumpster proves the EWS Recover Deleted Items round trip:
// MoveItem on a soft-deleted item (addressed from the recoverableitemsdeletions view)
// restores it into the client-chosen target folder, not its original parent, and the
// item leaves the dumpster.
func TestMoveItemRecoverFromDumpster(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if len(inbox) != 1 {
		st.Close()
		t.Fatalf("seed inbox = %d, want 1", len(inbox))
	}
	if err := st.SoftDeleteMessage(int64(mapi.PrivateFIDInbox), inbox[0].UID); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()

	// Locate the soft-deleted item in the dumpster to get its id.
	_, out := soapPost(t, ts, findItemReq("recoverableitemsdeletions"), true)
	m := itemIDRE.FindStringSubmatch(out)
	if len(m) != 2 {
		t.Fatalf("recoverable FindItem returned no ItemId: %s", out)
	}

	// Recover it into Drafts, a different folder than its original Inbox parent.
	_, mv := soapPost(t, ts, moveItemReq(m[1], "drafts", ""), true)
	if !strings.Contains(mv, `ResponseClass="Success"`) {
		t.Fatalf("MoveItem recover not success: %s", mv)
	}

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if drafts, _ := st2.ListMessages(int64(mapi.PrivateFIDDraft)); len(drafts) != 1 {
		t.Errorf("drafts after recover = %d, want 1 (restored to the chosen target)", len(drafts))
	}
	if live, _ := st2.ListMessages(int64(mapi.PrivateFIDInbox)); len(live) != 0 {
		t.Errorf("inbox after recover = %d, want 0 (restored to Drafts, not the original parent)", len(live))
	}
	if dump, _ := st2.ListAllSoftDeleted(); len(dump) != 0 {
		t.Errorf("dumpster after recover = %d, want 0 (item left the dumpster)", len(dump))
	}
}
