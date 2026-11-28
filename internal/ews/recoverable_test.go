package ews

import (
	"net/http"
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
	inbox := mustListMessages(t, st, int64(mapi.PrivateFIDInbox))
	if len(inbox) != 1 {
		mustCloseStore(t, st)
		t.Fatalf("seed inbox = %d, want 1", len(inbox))
	}
	if err := st.SoftDeleteMessage(int64(mapi.PrivateFIDInbox), inbox[0].UID); err != nil {
		mustCloseStore(t, st)
		t.Fatal(err)
	}
	mustCloseStore(t, st)

	// The live inbox no longer lists it.
	liveResp, outLive := soapPost(t, ts, findItemReq("inbox"), true)
	wantEq(t, "live Inbox FindItem status", liveResp.StatusCode, http.StatusOK)
	if strings.Contains(outLive, "Hello EWS") {
		t.Errorf("soft-deleted item still listed in the live inbox FindItem: %s", outLive)
	}

	// Unauthenticated dumpster FindItem is refused cleanly.
	unauthResp, unauthOut := soapPost(t, ts, findItemReq("recoverableitemsdeletions"), false)
	wantEq(t, "unauth recoverable FindItem status", unauthResp.StatusCode, http.StatusUnauthorized)
	wantNotContains(t, "unauth recoverable FindItem body", unauthOut, "Hello EWS")

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
	inbox := mustListMessages(t, st, int64(mapi.PrivateFIDInbox))
	if len(inbox) != 1 {
		mustCloseStore(t, st)
		t.Fatalf("seed inbox = %d, want 1", len(inbox))
	}
	if err := st.SoftDeleteMessage(int64(mapi.PrivateFIDInbox), inbox[0].UID); err != nil {
		mustCloseStore(t, st)
		t.Fatal(err)
	}
	mustCloseStore(t, st)

	// Locate the soft-deleted item in the dumpster to get its id.
	findResp, out := soapPost(t, ts, findItemReq("recoverableitemsdeletions"), true)
	wantEq(t, "dumpster FindItem status", findResp.StatusCode, http.StatusOK)
	m := itemIDRE.FindStringSubmatch(out)
	if len(m) != 2 {
		t.Fatalf("recoverable FindItem returned no ItemId: %s", out)
	}

	// Recover it into Drafts, a different folder than its original Inbox parent.
	mvResp, mv := soapPost(t, ts, moveItemReq(m[1], "drafts", ""), true)
	wantEq(t, "MoveItem recover status", mvResp.StatusCode, http.StatusOK)
	if !strings.Contains(mv, `ResponseClass="Success"`) {
		t.Fatalf("MoveItem recover not success: %s", mv)
	}

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustCloseStore(t, st2)
	if drafts := mustListMessages(t, st2, int64(mapi.PrivateFIDDraft)); len(drafts) != 1 {
		t.Errorf("drafts after recover = %d, want 1 (restored to the chosen target)", len(drafts))
	}
	if live := mustListMessages(t, st2, int64(mapi.PrivateFIDInbox)); len(live) != 0 {
		t.Errorf("inbox after recover = %d, want 0 (restored to Drafts, not the original parent)", len(live))
	}
	if dump := mustListAllSoftDeleted(t, st2); len(dump) != 0 {
		t.Errorf("dumpster after recover = %d, want 0 (item left the dumpster)", len(dump))
	}
}
