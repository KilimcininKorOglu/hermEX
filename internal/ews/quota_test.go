package ews

import (
	"encoding/base64"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// setReceiveQuota sets the mailbox's receive quota in KiB.
func setReceiveQuota(t *testing.T, dir string, kb uint32) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustCloseStore(t, st)
	if err := st.SetQuota(objectstore.QuotaLimits{ReceiveKB: kb}); err != nil {
		t.Fatal(err)
	}
}

// TestAWriteIntoAFullMailboxIsQuotaExceeded fills a mailbox past its receive
// quota, past which Exchange lets nothing new into it. Each EWS write that adds
// to the mailbox must answer [MS-OXWSCDATA] ErrorQuotaExceeded: CreateItem,
// CreateAttachment and CopyItem. A MoveItem inside the mailbox adds nothing, so a
// full mailbox can still file its mail away to free space.
func TestAWriteIntoAFullMailboxIsQuotaExceeded(t *testing.T) {
	big := "From: a@hermex.test\r\nTo: alice@hermex.test\r\nSubject: big\r\n\r\n" + strings.Repeat("x", 4096) + "\r\n"
	ts, dir := seededWithMessage(t, big)
	src := createUserFolder(t, ts, "inbox", "Src")
	dst := createUserFolder(t, ts, "inbox", "Dst")
	setReceiveQuota(t, dir, 1)
	item := parentItemID(t, ts)

	writes := map[string]string{
		"CreateItem":       createItemReq("SaveOnly", "bob@hermex.test", "draft", "body"),
		"CreateAttachment": createAttachmentReq(item, "a.txt", "text/plain", base64.StdEncoding.EncodeToString([]byte("x"))),
		"CopyItem":         copyItemReq(item, "drafts"),
		"CopyFolder":       moveCopyReq("CopyFolder", dst, src),
	}
	for op, req := range writes {
		if _, out := soapPost(t, ts, req, true); !strings.Contains(out, "ErrorQuotaExceeded") {
			t.Errorf("%s into a full mailbox: want ErrorQuotaExceeded, got %s", op, out)
		}
	}
	// CreateItem SaveOnly and CopyItem both target Drafts, so a refused write
	// that still stored something shows up there.
	if n := folderCount(t, dir, int64(mapi.PrivateFIDDraft)); n != 0 {
		t.Errorf("Drafts holds %d messages after the refused writes, want 0", n)
	}
	if _, out := soapPost(t, ts, moveItemReq(item, "drafts", ""), true); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("MoveItem inside a full mailbox must still succeed: %s", out)
	}

	// The same writes are served once the quota is lifted, so the refusal above
	// comes from the quota and not from the requests.
	setReceiveQuota(t, dir, 0)
	if _, out := soapPost(t, ts, createItemReq("SaveOnly", "bob@hermex.test", "draft", "body"), true); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("CreateItem with no quota set: %s", out)
	}
}
