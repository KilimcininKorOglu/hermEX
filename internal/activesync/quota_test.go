package activesync

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// fillPastQuota stores a 4 KiB message and sets a 1 KiB receive quota, the state
// of a mailbox that is full.
func fillPastQuota(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	big := "Subject: big\r\n\r\n" + strings.Repeat("x", 4096)
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(big), time.Unix(3, 0), 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuota(objectstore.QuotaLimits{ReceiveKB: 1}); err != nil {
		t.Fatal(err)
	}
}

// TestSyncAddIntoAFullMailboxIsRefused adds a note from the device to a mailbox
// past its receive quota, past which nothing new may enter it. MS-ASCMD Status
// (Sync) 9 is the item-level "cannot be completed" whose stated cause is an
// account out of disk space: the Add must carry it, with no ServerId, and nothing
// may be stored.
func TestSyncAddIntoAFullMailboxIsRefused(t *testing.T) {
	ts, dir := seededServer(t)
	postCommand(t, ts, "Sync", ntReq("0"))
	fillPastQuota(t, dir)

	add := wbxml.Elem(wbxml.ASAdd, wbxml.Str(wbxml.ASClientID, "cli-1"),
		wbxml.Elem(wbxml.ASData, wbxml.Str(wbxml.NTSubject, "Idea")))
	_, root := postCommand(t, ts, "Sync", ntReq("1", add))
	addResp := respColl(t, root).Child(wbxml.ASResponses).Child(wbxml.ASAdd)
	if addResp == nil {
		t.Fatal("the refused Add got no response at all")
	}
	if got := addResp.ChildText(wbxml.ASStatus); got != strconv.Itoa(9) {
		t.Errorf("Add status = %q, want 9", got)
	}
	if sid := addResp.ChildText(wbxml.ASServerID); sid != "" {
		t.Errorf("a refused Add was given ServerId %q", sid)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	notes, err := st.ListFolderObjects(int64(mapi.PrivateFIDNotes))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Errorf("the refused note was stored: %d notes", len(notes))
	}
}
