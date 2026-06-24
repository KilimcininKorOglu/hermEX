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
