package ews

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// folderSeen reports each item's read state in a folder, in list order.
func folderSeen(t *testing.T, dir string, fid int64) []bool {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(fid)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]bool, len(msgs))
	for i, m := range msgs {
		out[i] = m.Flags&objectstore.FlagSeen != 0
	}
	return out
}

func markAllReadBody(read bool, distinguished, mailbox string) string {
	rf := "false"
	if read {
		rf = "true"
	}
	dfid := `<t:DistinguishedFolderId Id="` + distinguished + `">`
	if mailbox != "" {
		dfid += `<t:Mailbox><t:EmailAddress>` + mailbox + `</t:EmailAddress></t:Mailbox>`
	}
	dfid += `</t:DistinguishedFolderId>`
	return `<MarkAllItemsAsRead xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<ReadFlag>` + rf + `</ReadFlag>` +
		`<SuppressReadReceipts>true</SuppressReadReceipts>` +
		`<FolderIds>` + dfid + `</FolderIds></MarkAllItemsAsRead>`
}

// TestMarkAllItemsAsReadTrue proves every unread item in the folder becomes read.
func TestMarkAllItemsAsReadTrue(t *testing.T) {
	ts, dir := seededWithMessage(t,
		"Subject: one\r\n\r\nbody one\r\n",
		"Subject: two\r\n\r\nbody two\r\n")
	for _, seen := range folderSeen(t, dir, int64(mapi.PrivateFIDInbox)) {
		if seen {
			t.Fatal("seeded messages should start unread")
		}
	}

	_, resp := soapPost(t, ts, wrapRequest(markAllReadBody(true, "inbox", "")), true)
	if !strings.Contains(resp, `ResponseClass="Success"`) {
		t.Fatalf("MarkAllItemsAsRead did not succeed: %s", resp)
	}

	got := folderSeen(t, dir, int64(mapi.PrivateFIDInbox))
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	for i, seen := range got {
		if !seen {
			t.Errorf("item %d still unread after ReadFlag=true", i)
		}
	}
}

// TestMarkAllItemsAsReadFalse proves a ReadFlag=false marks read items unread.
func TestMarkAllItemsAsReadFalse(t *testing.T) {
	ts, dir := seededWithMessage(t, "Subject: one\r\n\r\nbody\r\n")
	soapPost(t, ts, wrapRequest(markAllReadBody(true, "inbox", "")), true)  // read
	soapPost(t, ts, wrapRequest(markAllReadBody(false, "inbox", "")), true) // unread

	got := folderSeen(t, dir, int64(mapi.PrivateFIDInbox))
	if len(got) != 1 || got[0] {
		t.Errorf("item read state = %v, want one unread item", got)
	}
}

// TestMarkAllItemsAsReadForeignMailboxDenied is the OWASP A01 gate: marking another
// user's mailbox is refused and the caller's own items are untouched.
func TestMarkAllItemsAsReadForeignMailboxDenied(t *testing.T) {
	ts, dir := seededWithMessage(t, "Subject: keep unread\r\n\r\nbody\r\n")

	_, resp := soapPost(t, ts, wrapRequest(markAllReadBody(true, "inbox", "bob@hermex.test")), true)
	if !strings.Contains(resp, "ErrorAccessDenied") {
		t.Errorf("marking a foreign mailbox not denied: %s", resp)
	}
	if got := folderSeen(t, dir, int64(mapi.PrivateFIDInbox)); len(got) != 1 || got[0] {
		t.Errorf("caller's item was marked despite the denial: %v", got)
	}
}
