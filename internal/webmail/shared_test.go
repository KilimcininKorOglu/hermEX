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
	supportDir string // the shared store alice may open
	teamFID    int64  // a folder alice may see and read
	privFID    int64  // a folder alice has no grant on
	teamUID    uint32 // the message seeded in the Team folder
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

	auth := directory.StaticAccounts{
		"alice@hermex.test":   {Password: "secret", MailboxPath: aliceDir},
		"support@hermex.test": {Password: "x", MailboxPath: supportDir, Shared: true},
		"secret@hermex.test":  {Password: "x", MailboxPath: secretDir, Shared: true},
	}
	srv, err := NewServer(auth, auth, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	srv.Shared = auth
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, sharedEnv{supportDir: supportDir, teamFID: team, privFID: priv, teamUID: info.UID}
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
