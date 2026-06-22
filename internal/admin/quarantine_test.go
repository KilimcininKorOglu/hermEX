package admin

import (
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestQuarantineListReleaseDelete proves the admin quarantine view lists a user's
// Junk messages by metadata, that release actually moves a message to the inbox
// (not merely drops it from Junk), and that delete removes a message from both
// folders. The "present in the inbox after release" check is the one that
// distinguishes a real move from a silent drop.
func TestQuarantineListReleaseDelete(t *testing.T) {
	tmp := t.TempDir()
	mbox := filepath.Join(tmp, "alice")

	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Now()
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDJunk), []byte("Subject: spam one\r\n\r\nbuy now"), when, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDJunk), []byte("Subject: spam two\r\n\r\ndiscount"), when, 0); err != nil {
		t.Fatal(err)
	}
	junk, _ := st.ListMessages(int64(mapi.PrivateFIDJunk))
	st.Close()
	if len(junk) != 2 {
		t.Fatalf("seeded Junk has %d messages, want 2", len(junk))
	}
	releaseUID, deleteUID := junk[0].UID, junk[1].UID

	d := &fakeDir{
		authOK:     true,
		uid:        7,
		roles:      []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{Maildir: mbox},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	// List shows both Junk messages by subject (metadata only).
	resp := authedGET(t, ts, "/admin/ui/users/alice@test/quarantine", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if page := string(body); !strings.Contains(page, "spam one") || !strings.Contains(page, "spam two") {
		t.Fatalf("quarantine list missing seeded subjects:\n%s", page)
	}

	// Release the first message → it leaves Junk and lands in the inbox.
	rel := htmxPOST(t, ts, "/admin/ui/users/alice@test/quarantine/release", session, csrf,
		url.Values{"uid": {strconv.FormatUint(uint64(releaseUID), 10)}})
	rel.Body.Close()

	st2, _ := objectstore.Open(mbox)
	junkAfter, _ := st2.ListMessages(int64(mapi.PrivateFIDJunk))
	inboxAfter, _ := st2.ListMessages(int64(mapi.PrivateFIDInbox))
	st2.Close()
	if len(junkAfter) != 1 {
		t.Errorf("after release Junk has %d, want 1", len(junkAfter))
	}
	if len(inboxAfter) != 1 || inboxAfter[0].Subject != "spam one" {
		t.Errorf("released message must be in the inbox; inbox=%+v", inboxAfter)
	}

	// Delete the remaining message → gone from Junk, inbox untouched.
	del := htmxPOST(t, ts, "/admin/ui/users/alice@test/quarantine/delete", session, csrf,
		url.Values{"uid": {strconv.FormatUint(uint64(deleteUID), 10)}})
	del.Body.Close()

	st3, _ := objectstore.Open(mbox)
	junkFinal, _ := st3.ListMessages(int64(mapi.PrivateFIDJunk))
	inboxFinal, _ := st3.ListMessages(int64(mapi.PrivateFIDInbox))
	st3.Close()
	if len(junkFinal) != 0 {
		t.Errorf("after delete Junk has %d, want 0", len(junkFinal))
	}
	if len(inboxFinal) != 1 {
		t.Errorf("delete must not touch the inbox: inbox has %d, want 1", len(inboxFinal))
	}
}
