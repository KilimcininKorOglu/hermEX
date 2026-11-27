package webmail2api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// copyMailbox stores one seen message in the inbox and creates an empty Project
// folder, returning the mailbox, the message's UID and the folder id.
func copyMailbox(t *testing.T) (string, uint32, int64) {
	t.Helper()
	dir := freshMailbox(t)
	st := openMailbox(t, dir)
	pfid, err := st.CreateFolder(nil, "Project")
	mustNoErr(t, "create folder", err)
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: a@b.test\r\nSubject: keep me\r\n\r\nhi\r\n"), time.Now(), objectstore.FlagSeen)
	mustNoErr(t, "append", err)
	return dir, info.UID, pfid
}

// folderCount is how many messages a folder holds.
func folderCount(t *testing.T, dir string, fid int64) int {
	t.Helper()
	msgs, err := openMailbox(t, dir).ListMessages(fid)
	mustNoErr(t, "list folder", err)
	return len(msgs)
}

// TestCopyMessageToFolder proves POST /mail/copy ports the old webmail's
// copy-to-folder: the message lands in the target folder while the original
// stays put (a copy, not a move), and the copy keeps the source flags
// (RFC 9051 section 6.4.7: COPY preserves the flags).
func TestCopyMessageToFolder(t *testing.T) {
	dir, uid, pfid := copyMailbox(t)
	do := apiHarnessFor(t, dir)
	wantStatus(t, "copy", do(http.MethodPost, "/api/v1/mail/copy",
		`{"id":"inbox:`+strconv.FormatUint(uint64(uid), 10)+`","to":"`+customSlug(pfid)+`"}`), http.StatusOK)

	wantEq(t, "inbox after the copy (original kept)", folderCount(t, dir, int64(mapi.PrivateFIDInbox)), 1)
	copies, err := openMailbox(t, dir).ListMessages(pfid)
	mustNoErr(t, "list project", err)
	if len(copies) != 1 {
		t.Fatalf("Project has %d messages, want 1 (the copy)", len(copies))
	}
	if copies[0].Flags&objectstore.FlagSeen == 0 {
		t.Errorf("copied message lost its \\Seen flag")
	}
}

// TestCopyRefusesWhatIsNotThere: a copy naming a folder or a message that does
// not exist answers an error and creates nothing anywhere.
func TestCopyRefusesWhatIsNotThere(t *testing.T) {
	dir, uid, pfid := copyMailbox(t)
	do := apiHarnessFor(t, dir)
	id := "inbox:" + strconv.FormatUint(uint64(uid), 10)
	for _, body := range []string{
		`{"id":"` + id + `","to":"` + customSlug(pfid+1000) + `"}`,
		`{"id":"` + id + `","to":"no-such-folder"}`,
		`{"id":"not-an-id","to":"` + customSlug(pfid) + `"}`,
	} {
		if rec := do(http.MethodPost, "/api/v1/mail/copy", body); rec.Code == http.StatusOK {
			t.Errorf("copy %s answered 200", body)
		}
	}
	// A message that is not there is not found, not a server failure.
	wantStatus(t, "copy of a missing message", do(http.MethodPost, "/api/v1/mail/copy",
		`{"id":"inbox:`+strconv.FormatUint(uint64(uid)+100, 10)+`","to":"`+customSlug(pfid)+`"}`), http.StatusNotFound)
	wantEq(t, "inbox after the refused copies", folderCount(t, dir, int64(mapi.PrivateFIDInbox)), 1)
	wantEq(t, "Project after the refused copies", folderCount(t, dir, pfid), 0)
}
