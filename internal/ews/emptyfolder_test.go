package ews

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// inboxCount opens the mailbox and returns the live message count of a folder.
func folderCount(t *testing.T, dir string, fid int64) int {
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
	return len(msgs)
}

func emptyFolderBody(deleteType string, subfolders bool, distinguished string, mailbox string) string {
	sub := "false"
	if subfolders {
		sub = "true"
	}
	dfid := `<t:DistinguishedFolderId Id="` + distinguished + `">`
	if mailbox != "" {
		dfid += `<t:Mailbox><t:EmailAddress>` + mailbox + `</t:EmailAddress></t:Mailbox>`
	}
	dfid += `</t:DistinguishedFolderId>`
	return `<EmptyFolder DeleteType="` + deleteType + `" DeleteSubFolders="` + sub + `" xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<FolderIds>` + dfid + `</FolderIds></EmptyFolder>`
}

// TestEmptyFolderHardDelete proves emptying the Inbox removes its items.
func TestEmptyFolderHardDelete(t *testing.T) {
	ts, dir := seededWithMessage(t,
		"Subject: one\r\n\r\nbody one\r\n",
		"Subject: two\r\n\r\nbody two\r\n")
	if n := folderCount(t, dir, int64(mapi.PrivateFIDInbox)); n != 2 {
		t.Fatalf("seed: %d messages, want 2", n)
	}

	_, resp := soapPost(t, ts, wrapRequest(emptyFolderBody("HardDelete", false, "inbox", "")), true)
	if !strings.Contains(resp, `ResponseClass="Success"`) || !strings.Contains(resp, "NoError") {
		t.Fatalf("EmptyFolder did not succeed: %s", resp)
	}

	if n := folderCount(t, dir, int64(mapi.PrivateFIDInbox)); n != 0 {
		t.Errorf("after EmptyFolder: %d messages, want 0", n)
	}
}

// TestEmptyFolderMoveToDeletedItems proves the MoveToDeletedItems disposition
// moves the items into Deleted Items rather than removing them.
func TestEmptyFolderMoveToDeletedItems(t *testing.T) {
	ts, dir := seededWithMessage(t,
		"Subject: one\r\n\r\nbody one\r\n",
		"Subject: two\r\n\r\nbody two\r\n")

	_, resp := soapPost(t, ts, wrapRequest(emptyFolderBody("MoveToDeletedItems", false, "inbox", "")), true)
	if !strings.Contains(resp, `ResponseClass="Success"`) {
		t.Fatalf("EmptyFolder did not succeed: %s", resp)
	}

	if n := folderCount(t, dir, int64(mapi.PrivateFIDInbox)); n != 0 {
		t.Errorf("Inbox after move: %d messages, want 0", n)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDDeletedItems)); n != 2 {
		t.Errorf("Deleted Items after move: %d messages, want 2", n)
	}
}

// TestEmptyFolderForeignMailboxDenied is the OWASP A01 gate: emptying a folder in
// another user's mailbox (a Mailbox child on the folder id) is refused, and the
// caller's own items are untouched.
func TestEmptyFolderForeignMailboxDenied(t *testing.T) {
	ts, dir := seededWithMessage(t, "Subject: keep me\r\n\r\nbody\r\n")

	_, resp := soapPost(t, ts, wrapRequest(emptyFolderBody("HardDelete", false, "inbox", "bob@hermex.test")), true)
	if !strings.Contains(resp, "ErrorAccessDenied") {
		t.Errorf("emptying a foreign mailbox not denied: %s", resp)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("caller's Inbox was emptied despite the denial: %d messages, want 1", n)
	}
}
