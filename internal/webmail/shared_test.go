package webmail

import (
	"net/http/httptest"
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

// sharedEnv carries the fixtures a shared-mailbox test asserts against.
type sharedEnv struct {
	supportDir string // a shared store alice may read (reviewer on Team)
	teamFID    int64  // a folder alice may see and read but not modify
	privFID    int64  // a folder alice has no grant on
	teamUID    uint32 // the message seeded in the Team folder
	ownedDir   string // a shared store alice owns (full read-write)
	ownedUID   uint32 // a message seeded in the owned store's Inbox
}

// newSharedWebmail builds a webmail server for alice@hermex.test with two shared
// mailboxes wired: support@ (alice holds a read grant on its Team folder, none on
// Private) and secret@ (alice has no grant at all). It returns the test server and
// the fixtures.
func newSharedWebmail(t *testing.T) (*httptest.Server, sharedEnv) {
	t.Helper()
	root := t.TempDir()

	aliceDir := filepath.Join(root, "alice")
	if st, err := objectstore.Open(aliceDir); err != nil {
		t.Fatal(err)
	} else {
		st.Close()
	}

	supportDir := filepath.Join(root, "support")
	sst, err := objectstore.Open(supportDir)
	if err != nil {
		t.Fatal(err)
	}
	team, err := sst.CreateFolder(nil, "Team")
	if err != nil {
		t.Fatal(err)
	}
	priv, err := sst.CreateFolder(nil, "Private")
	if err != nil {
		t.Fatal(err)
	}
	if err := sst.ModifyPermissions(team, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: "alice@hermex.test", Rights: mapi.FrightsVisible | mapi.FrightsReadAny},
	}); err != nil {
		t.Fatal(err)
	}
	info, err := sst.AppendMessage(team, []byte("Subject: SharedHi\r\n\r\nshared body here"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	sst.Close()

	// A second shared mailbox alice has no access to: it must never surface to her.
	secretDir := filepath.Join(root, "secret")
	if st, err := objectstore.Open(secretDir); err != nil {
		t.Fatal(err)
	} else {
		st.Close()
	}

	// A third shared mailbox alice OWNS (additional store owner): full read-write.
	ownedDir := filepath.Join(root, "owned")
	ost, err := objectstore.Open(ownedDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ost.SetStoreOwners([]string{"alice@hermex.test"}); err != nil {
		t.Fatal(err)
	}
	oinfo, err := ost.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("Subject: OwnedHi\r\n\r\nowned body here"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	ost.Close()

	auth := directory.StaticAccounts{
		"alice@hermex.test":   {Password: "secret", MailboxPath: aliceDir},
		"support@hermex.test": {Password: "x", MailboxPath: supportDir, Shared: true},
		"secret@hermex.test":  {Password: "x", MailboxPath: secretDir, Shared: true},
		"owned@hermex.test":   {Password: "x", MailboxPath: ownedDir, Shared: true},
	}
	srv, err := NewServer(auth, auth, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	srv.Shared = auth
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, sharedEnv{
		supportDir: supportDir, teamFID: team, privFID: priv, teamUID: info.UID,
		ownedDir: ownedDir, ownedUID: oinfo.UID,
	}
}

// TestSharedMailboxSidebar proves the mailbox sidebar lists a shared mailbox the
// user may open with only the folders they may see, and never one they cannot.
func TestSharedMailboxSidebar(t *testing.T) {
	ts, _ := newSharedWebmail(t)
	c := authedClient(t, ts)

	code, body := get(t, c, ts.URL+"/mail")
	if code != 200 {
		t.Fatalf("GET /mail = %d", code)
	}
	if !strings.Contains(body, "support@hermex.test") {
		t.Errorf("sidebar missing the accessible shared mailbox support@hermex.test")
	}
	if !strings.Contains(body, "folder=Team") || !strings.Contains(body, "mbox=support") {
		t.Errorf("sidebar missing the visible shared folder link (folder=Team&mbox=support)")
	}
	if strings.Contains(body, "Private") {
		t.Errorf("sidebar leaked the Private folder (alice has no grant on it)")
	}
	if strings.Contains(body, "secret@hermex.test") {
		t.Errorf("sidebar leaked secret@hermex.test (alice has no access to it)")
	}
}

// TestSharedMailboxReadBrowse proves a user may list and read a shared folder's
// messages, that the view is read-only (no bulk/write controls), and that the
// read does not mark the shared message \Seen.
func TestSharedMailboxReadBrowse(t *testing.T) {
	ts, env := newSharedWebmail(t)
	c := authedClient(t, ts)

	// List the shared folder: its message shows, but no write controls are rendered.
	code, body := get(t, c, ts.URL+"/mail?folder=Team&mbox=support@hermex.test")
	if code != 200 || !strings.Contains(body, "SharedHi") {
		t.Fatalf("open shared Team (%d): message listed? %v", code, strings.Contains(body, "SharedHi"))
	}
	if strings.Contains(body, `id="bulkform"`) {
		t.Errorf("shared list rendered the bulk write toolbar (should be read-only)")
	}
	if !strings.Contains(body, "mbox=support") {
		t.Errorf("shared list row links dropped the mbox context")
	}

	// Read the message: body shows, reply/delete controls are hidden.
	uid := strconv.FormatUint(uint64(env.teamUID), 10)
	code, body = get(t, c, ts.URL+"/message?folder=Team&uid="+uid+"&mbox=support@hermex.test")
	if code != 200 || !strings.Contains(body, "shared body here") {
		t.Fatalf("read shared message (%d): body present? %v", code, strings.Contains(body, "shared body here"))
	}
	if strings.Contains(body, "op=delete") || strings.Contains(body, "action=reply") {
		t.Errorf("shared reader rendered write controls (should be read-only)")
	}

	// The read must not have marked the shared message \Seen (that is a write).
	sst, err := objectstore.Open(env.supportDir)
	if err != nil {
		t.Fatal(err)
	}
	defer sst.Close()
	if flags, err := sst.MessageFlags(env.teamFID, env.teamUID); err != nil {
		t.Fatal(err)
	} else if flags&objectstore.FlagSeen != 0 {
		t.Errorf("reading a shared message marked it \\Seen (a write into the shared store)")
	}
}

// TestSharedMailboxReadGate proves the per-folder read ACL is enforced server-side:
// a folder the caller has no grant on is denied even with a valid mbox.
func TestSharedMailboxReadGate(t *testing.T) {
	ts, _ := newSharedWebmail(t)
	c := authedClient(t, ts)
	if code, _ := get(t, c, ts.URL+"/message?folder=Private&uid=1&mbox=support@hermex.test"); code != 404 {
		t.Errorf("read of an ungranted shared folder = %d, want 404", code)
	}
}

// TestSharedMailboxAccessGate proves a shared mailbox the caller cannot open, and
// an address that is not a shared mailbox at all, both 404 — and never reveal
// which case it was.
func TestSharedMailboxAccessGate(t *testing.T) {
	ts, _ := newSharedWebmail(t)
	c := authedClient(t, ts)
	if code, _ := get(t, c, ts.URL+"/mail?folder=INBOX&mbox=secret@hermex.test"); code != 404 {
		t.Errorf("open a shared mailbox with no grant = %d, want 404", code)
	}
	if code, _ := get(t, c, ts.URL+"/mail?folder=INBOX&mbox=nobody@hermex.test"); code != 404 {
		t.Errorf("open a non-shared address = %d, want 404 (server-derived path, IDOR-safe)", code)
	}
}

// TestSharedMailboxWritesDenied proves every mutating endpoint rejects a shared-
// scoped request outright (403), so a control left in a shared view — or a forged
// request — can never misfire against the caller's own store before the write path
// is authorized.
func TestSharedMailboxWritesDenied(t *testing.T) {
	ts, _ := newSharedWebmail(t)
	c := authedClient(t, ts)
	for _, u := range []string{
		"/action?folder=Team&uid=1&op=delete&mbox=support@hermex.test",
		"/bulk?mbox=support@hermex.test",
		"/folder?mbox=support@hermex.test",
		"/export?mbox=support@hermex.test",
	} {
		if code, _ := postForm(t, c, ts.URL+u, url.Values{}); code != 403 {
			t.Errorf("POST %s = %d, want 403 (shared writes denied here)", u, code)
		}
	}
	// A not-yet-shared-aware read endpoint likewise refuses an mbox it cannot honor.
	if code, _ := get(t, c, ts.URL+"/search?q=x&mbox=support@hermex.test"); code != 403 {
		t.Errorf("GET /search with mbox = %d, want 403", code)
	}
}

// TestSharedMailboxOwnerActions proves a user who owns a shared mailbox may act
// on its messages: a flag is applied and a delete re-files the message out of the
// Inbox.
func TestSharedMailboxOwnerActions(t *testing.T) {
	ts, env := newSharedWebmail(t)
	c := authedClient(t, ts)
	uid := strconv.FormatUint(uint64(env.ownedUID), 10)

	// Flag the message: the action is authorized (owner) and the flag persists.
	if code, _ := postForm(t, c, ts.URL+"/action?folder=INBOX&uid="+uid+"&op=flag&color=6&mbox=owned@hermex.test", url.Values{}); code != 200 {
		t.Fatalf("owner flag = %d, want 200", code)
	}
	ost, err := objectstore.Open(env.ownedDir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ost.MessageByUID(int64(mapi.PrivateFIDInbox), env.ownedUID)
	if err != nil {
		ost.Close()
		t.Fatalf("owned message gone before delete: %v", err)
	}
	if f, err := ost.GetFollowupFlag(m.ID); err != nil || f.Color != 6 {
		ost.Close()
		t.Fatalf("owner flag not applied: color=%d err=%v", f.Color, err)
	}
	ost.Close()

	// Delete the message: it leaves the Inbox (re-filed to Deleted Items).
	if code, _ := postForm(t, c, ts.URL+"/action?folder=INBOX&uid="+uid+"&op=delete&mbox=owned@hermex.test", url.Values{}); code != 200 {
		t.Fatalf("owner delete = %d, want 200", code)
	}
	ost, err = objectstore.Open(env.ownedDir)
	if err != nil {
		t.Fatal(err)
	}
	defer ost.Close()
	if _, err := ost.MessageByUID(int64(mapi.PrivateFIDInbox), env.ownedUID); err == nil {
		t.Errorf("deleted message is still in the owned Inbox")
	}
}

// TestSharedMailboxReviewerCannotWrite proves a read-only (reviewer) grant cannot
// drive a write: a flag or delete on such a folder is refused and the message is
// left untouched.
func TestSharedMailboxReviewerCannotWrite(t *testing.T) {
	ts, env := newSharedWebmail(t)
	c := authedClient(t, ts)
	uid := strconv.FormatUint(uint64(env.teamUID), 10)

	for _, op := range []string{"flag&color=6", "delete"} {
		if code, _ := postForm(t, c, ts.URL+"/action?folder=Team&uid="+uid+"&op="+op+"&mbox=support@hermex.test", url.Values{}); code != 403 {
			t.Errorf("reviewer op=%s = %d, want 403", op, code)
		}
	}
	// The refused flag must not have touched the message.
	sst, err := objectstore.Open(env.supportDir)
	if err != nil {
		t.Fatal(err)
	}
	defer sst.Close()
	m, err := sst.MessageByUID(env.teamFID, env.teamUID)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := sst.GetFollowupFlag(m.ID); err == nil && f.Color != 0 {
		t.Errorf("a refused write still flagged the reviewer's message (color=%d)", f.Color)
	}
}

// TestSharedMailboxWritableRendersControls proves the view shows per-message write
// controls only where the caller may write: present in an owned shared folder,
// absent in a read-only (reviewer) one.
func TestSharedMailboxWritableRendersControls(t *testing.T) {
	ts, _ := newSharedWebmail(t)
	c := authedClient(t, ts)

	code, body := get(t, c, ts.URL+"/mail?folder=INBOX&mbox=owned@hermex.test")
	if code != 200 || !strings.Contains(body, "OwnedHi") {
		t.Fatalf("open owned Inbox (%d): message listed? %v", code, strings.Contains(body, "OwnedHi"))
	}
	if !strings.Contains(body, "op=delete") || !strings.Contains(body, "mbox=owned") {
		t.Errorf("owned (writable) list missing per-message action controls carrying mbox")
	}

	if _, body := get(t, c, ts.URL+"/mail?folder=Team&mbox=support@hermex.test"); strings.Contains(body, "op=delete") {
		t.Errorf("read-only (reviewer) list rendered a delete control")
	}
}
