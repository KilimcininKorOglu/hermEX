package ews

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// fakeJunkDir is a static directory that also records personal block-list edits,
// so the MarkAsJunk handler's capability assertion succeeds without a database.
type fakeJunkDir struct {
	directory.StaticAccounts
	blocked map[string]bool
}

func (f *fakeJunkDir) SetRecipientRule(_, pattern, action string) error {
	f.blocked[pattern] = action == directory.SenderBlock
	return nil
}

func (f *fakeJunkDir) DeleteRecipientRule(_, pattern string) (bool, error) {
	delete(f.blocked, pattern)
	return true, nil
}

// junkServer builds a server backed by a block-list-capable directory.
func junkServer(t *testing.T) (*httptest.Server, string, *fakeJunkDir) {
	t.Helper()
	dir := t.TempDir()
	d := &fakeJunkDir{
		StaticAccounts: directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}},
		blocked:        map[string]bool{},
	}
	ts := httptest.NewServer(NewServer(d, d, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, dir, d
}

// seedFromSender appends an Inbox message with a known From address.
func seedFromSender(t *testing.T, dir, from string) (mid int64, uid uint32) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := "From: " + from + "\r\nSubject: buy now\r\n\r\nbody\r\n"
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1718200000, 0), 0); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil || len(msgs) == 0 {
		t.Fatalf("seed list: %v", err)
	}
	last := msgs[len(msgs)-1]
	return last.ID, last.UID
}

func markAsJunkBody(isJunk, moveItem bool, itemID string) string {
	bf := func(b bool) string {
		if b {
			return "true"
		}
		return "false"
	}
	return `<MarkAsJunk IsJunk="` + bf(isJunk) + `" MoveItem="` + bf(moveItem) + `" xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<ItemIds><t:ItemId Id="` + itemID + `"/></ItemIds></MarkAsJunk>`
}

func inboxItemID(folderID, mid int64, uid uint32, mailbox string) string {
	return oxews.EncodeItemID(oxews.ItemID{FolderID: folderID, MessageID: mid, UID: uid, Mailbox: mailbox})
}

// TestMarkAsJunkBlocksAndMoves proves IsJunk=true with MoveItem=true blocks the
// sender and moves the message to the Junk folder.
func TestMarkAsJunkBlocksAndMoves(t *testing.T) {
	ts, dir, d := junkServer(t)
	mid, uid := seedFromSender(t, dir, "spammer@evil.test")
	id := inboxItemID(int64(mapi.PrivateFIDInbox), mid, uid, "")

	_, resp := soapPost(t, ts, wrapRequest(markAsJunkBody(true, true, id)), true)
	if !strings.Contains(resp, `ResponseClass="Success"`) || !strings.Contains(resp, "MovedItemId") {
		t.Fatalf("MarkAsJunk did not succeed with a MovedItemId: %s", resp)
	}
	if !d.blocked["spammer@evil.test"] {
		t.Error("sender was not added to the block list")
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDJunk)); n != 1 {
		t.Errorf("Junk folder has %d items, want 1", n)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDInbox)); n != 0 {
		t.Errorf("Inbox still has %d items, want 0", n)
	}
}

// TestMarkAsJunkBlockOnly proves IsJunk=true with MoveItem=false blocks the sender
// but leaves the message in place.
func TestMarkAsJunkBlockOnly(t *testing.T) {
	ts, dir, d := junkServer(t)
	mid, uid := seedFromSender(t, dir, "spammer@evil.test")
	id := inboxItemID(int64(mapi.PrivateFIDInbox), mid, uid, "")

	_, resp := soapPost(t, ts, wrapRequest(markAsJunkBody(true, false, id)), true)
	if !strings.Contains(resp, `ResponseClass="Success"`) {
		t.Fatalf("MarkAsJunk did not succeed: %s", resp)
	}
	if strings.Contains(resp, "MovedItemId") {
		t.Error("a no-move request must not return a MovedItemId")
	}
	if !d.blocked["spammer@evil.test"] {
		t.Error("sender was not blocked")
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("Inbox has %d items, want 1 (no move)", n)
	}
}

// TestMarkAsJunkForeignMailboxDenied is the OWASP A01 gate: an item in another
// mailbox is refused and nothing is blocked.
func TestMarkAsJunkForeignMailboxDenied(t *testing.T) {
	ts, dir, d := junkServer(t)
	mid, uid := seedFromSender(t, dir, "spammer@evil.test")
	id := inboxItemID(int64(mapi.PrivateFIDInbox), mid, uid, "bob@hermex.test")

	_, resp := soapPost(t, ts, wrapRequest(markAsJunkBody(true, true, id)), true)
	if !strings.Contains(resp, "ErrorAccessDenied") {
		t.Errorf("a foreign-mailbox item was not denied: %s", resp)
	}
	if len(d.blocked) != 0 {
		t.Errorf("a block rule was written for a denied request: %v", d.blocked)
	}
}
