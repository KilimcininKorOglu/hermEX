package imap

import (
	"bufio"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/publicfolder"
)

// pubPaths maps a domain to its own public-store directory under a test root.
type pubPaths struct{ root string }

func (p pubPaths) HomedirFor(domain string) string {
	return filepath.Join(p.root, "public", domain)
}

// emptyMailbox creates an empty private mailbox store at path.
func emptyMailbox(t *testing.T, path string) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatalf("open mailbox %s: %v", path, err)
	}
	st.Close()
}

func grantAnyone(t *testing.T, st *objectstore.Store, fid int64, rights uint32) {
	t.Helper()
	if err := st.ModifyPermissions(fid, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, MemberID: mapi.MemberIDDefault, Rights: rights},
	}); err != nil {
		t.Fatalf("grant anyone on %d: %v", fid, err)
	}
}

func grantUser(t *testing.T, st *objectstore.Store, fid int64, user string, rights uint32) {
	t.Helper()
	if err := st.ModifyPermissions(fid, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: user, Rights: rights},
	}); err != nil {
		t.Fatalf("grant %s on %d: %v", user, fid, err)
	}
}

// publicServer starts an IMAP server with public folders wired and returns its
// address so a test can dial one or more clients.
func publicServer(t *testing.T, accounts directory.StaticAccounts, pub *publicfolder.Service) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = (&Server{Auth: accounts, Hostname: "mail.test", Pub: pub}).Serve(ln) }()
	return ln.Addr().String()
}

// dialLogin dials the server and logs in, returning a ready client.
func dialLogin(t *testing.T, addr, user, pass string) *testClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")
	c.mustOK("login", "LOGIN "+user+" "+pass)
	return c
}

// doFull sends a command and returns the untagged lines and the full tagged
// completion line (so a test can inspect a response code like [READ-ONLY]).
func (c *testClient) doFull(tag, cmd string) (untagged []string, tagged string) {
	c.t.Helper()
	_, _ = fmt.Fprintf(c.conn, "%s %s\r\n", tag, cmd)
	for {
		l := c.line()
		if strings.HasPrefix(l, tag+" ") {
			return untagged, l
		}
		untagged = append(untagged, l)
	}
}

func hasLine(lines []string, substr string) bool {
	for _, l := range lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// TestIMAPPublicFolders walks the IMAP public folders surface as one domain user:
// NAMESPACE advertises the shared namespace, LIST is ACL-filtered, SELECT opens a
// readable public folder (read-only without post rights, read-write with them),
// APPEND is gated on post rights, and a posted message reads back from the public
// store (cross-store FETCH).
func TestIMAPPublicFolders(t *testing.T) {
	root := t.TempDir()
	mbox := filepath.Join(root, "alice")
	emptyMailbox(t, mbox)

	pub := seedPublicGrants(t, root)
	accounts := directory.StaticAccounts{"alice@local.test": {Password: "secret", MailboxPath: mbox}}
	c := dialLogin(t, publicServer(t, accounts, pub), "alice@local.test", "secret")

	// NAMESPACE advertises the public shared namespace.
	wantLine(t, "NAMESPACE", c.mustOK("ns", "NAMESPACE"), `("Public Folders/" "/")`)

	// LIST shows the folders alice may see (Announcements, Bulletin) and the
	// namespace container, but not Staff (granted only to bob).
	listUn := c.mustOK("l", `LIST "" "*"`)
	wantLine(t, "LIST", listUn, `"Public Folders/Announcements"`)
	wantLine(t, "LIST", listUn, `"Public Folders/Bulletin"`)
	wantNoLine(t, "LIST (alice has no grant on Staff)", listUn, "Staff")

	// A folder alice cannot see is not selectable.
	wantStatus(t, "SELECT Staff", c, "s0", `SELECT "Public Folders/Staff"`, "NO")

	// Announcements: anyone-Reviewer → selectable, but read-only (no post rights).
	wantSelect(t, "SELECT Announcements", c, "s1", `SELECT "Public Folders/Announcements"`, "[READ-ONLY]")
	wantEq(t, "APPEND to read-only Announcements",
		c.appendMsg("a1", `"Public Folders/Announcements"`, "Subject: x\r\n\r\nno"), "NO")

	// Bulletin: alice has the Create right (post) but not edit/delete. She may
	// APPEND, but the selection is read-only, a poster must not be able to modify
	// or delete others' messages.
	if status := c.appendMsg("a2", `"Public Folders/Bulletin"`, "Subject: hi\r\n\r\nhello world"); status != "OK" {
		t.Fatalf("APPEND to Bulletin = %s, want OK", status)
	}
	selUn := wantSelect(t, "SELECT Bulletin (poster, no edit/delete)", c, "s2", `SELECT "Public Folders/Bulletin"`, "[READ-ONLY]")
	wantLine(t, "SELECT Bulletin after the post", selUn, "1 EXISTS")
	// A poster cannot mutate existing items: STORE is refused on the read-only selection.
	wantStatus(t, "STORE on a poster's read-only public selection", c, "st", `STORE 1 +FLAGS (\Deleted)`, "NO")
	// The posted message still reads back from the public store (cross-store FETCH).
	wantLine(t, "FETCH from a public folder", c.mustOK("f", "FETCH 1 (BODY[TEXT])"), "hello world")

	// Team: alice is an Editor (edit/delete any) → read-write selection, STORE works.
	wantSelect(t, "SELECT Team (editor)", c, "s3", `SELECT "Public Folders/Team"`, "[READ-WRITE]")
	wantStatus(t, "STORE on an editor's public selection", c, "st2", `STORE 1 +FLAGS (\Flagged)`, "OK")

	// STATUS answers for a LIST-advertised public folder (clients poll it for badges).
	stUn, _ := c.doFull("status", `STATUS "Public Folders/Announcements" (MESSAGES UNSEEN)`)
	wantLine(t, "STATUS on a public folder", stUn, "MESSAGES")
}

// seedPublicGrants provisions one domain's public store with four folders whose
// grants cover the access tiers: anyone-readable, poster-only, another user's,
// and editor.
func seedPublicGrants(t *testing.T, root string) *publicfolder.Service {
	t.Helper()
	pub := publicfolder.New(pubPaths{root: root})
	mustNoErr(t, "provision the public store", pub.Provision("local.test"))
	ps, err := pub.OpenForDomain("local.test")
	mustNoErr(t, "open the public store", err)
	defer ps.Close()
	ann, _ := ps.CreateFolder(nil, "Announcements")
	bul, _ := ps.CreateFolder(nil, "Bulletin")
	staff, _ := ps.CreateFolder(nil, "Staff")
	team, _ := ps.CreateFolder(nil, "Team")
	grantAnyone(t, ps, ann, mapi.FrightsVisible|mapi.FrightsReadAny)                                      // read-only for everyone
	grantUser(t, ps, bul, "alice@local.test", mapi.FrightsVisible|mapi.FrightsReadAny|mapi.FrightsCreate) // alice may post, not modify
	grantUser(t, ps, staff, "bob@local.test", mapi.FrightsVisible|mapi.FrightsReadAny)                    // bob only
	grantUser(t, ps, team, "alice@local.test", mapi.RightsEditor)                                         // alice may edit/delete any
	_, err = ps.AppendMessage(team, []byte("Subject: t\r\n\r\nteam body"), time.Unix(2, 0), 0)
	mustNoErr(t, "seed the team message", err)
	return pub
}

// wantLine fails when the untagged responses carry no line with the fragment.
func wantLine(t *testing.T, what string, lines []string, want string) {
	t.Helper()
	if !hasLine(lines, want) {
		t.Errorf("%s is missing %q: %v", what, want, lines)
	}
}

// wantNoLine fails when the untagged responses carry a fragment they must not.
func wantNoLine(t *testing.T, what string, lines []string, unwanted string) {
	t.Helper()
	if hasLine(lines, unwanted) {
		t.Errorf("%s leaked %q: %v", what, unwanted, lines)
	}
}

// wantStatus runs one command and checks the tagged status it answers.
func wantStatus(t *testing.T, what string, c *testClient, tag, cmd, want string) {
	t.Helper()
	_, status := c.do(tag, cmd)
	wantEq(t, what, status, want)
}

// wantSelect runs a SELECT, requires it to succeed with the given access code,
// and returns its untagged responses.
func wantSelect(t *testing.T, what string, c *testClient, tag, cmd, access string) []string {
	t.Helper()
	un, tagged := c.doFull(tag, cmd)
	wantContains(t, what, tagged, "OK")
	wantContains(t, what+" access", tagged, access)
	return un
}

// TestIMAPPublicFolderTenantIsolation proves an IMAP client only ever sees its own
// domain's public folders: alice@local.test sees LocalNews and never OtherNews,
// carol@other.test the reverse, even though both domains are served by one process.
func TestIMAPPublicFolderTenantIsolation(t *testing.T) {
	root := t.TempDir()
	aliceBox := filepath.Join(root, "alice")
	carolBox := filepath.Join(root, "carol")
	emptyMailbox(t, aliceBox)
	emptyMailbox(t, carolBox)

	pub := publicfolder.New(pubPaths{root: root})
	for _, d := range []struct{ domain, folder string }{
		{"local.test", "LocalNews"},
		{"other.test", "OtherNews"},
	} {
		if err := pub.Provision(d.domain); err != nil {
			t.Fatal(err)
		}
		st, err := pub.OpenForDomain(d.domain)
		if err != nil {
			t.Fatal(err)
		}
		fid, _ := st.CreateFolder(nil, d.folder)
		grantAnyone(t, st, fid, mapi.FrightsVisible|mapi.FrightsReadAny)
		st.Close()
	}

	accounts := directory.StaticAccounts{
		"alice@local.test": {Password: "secret", MailboxPath: aliceBox},
		"carol@other.test": {Password: "secret", MailboxPath: carolBox},
	}
	addr := publicServer(t, accounts, pub)

	alice := dialLogin(t, addr, "alice@local.test", "secret")
	aliceList := alice.mustOK("l", `LIST "" "*"`)
	if !hasLine(aliceList, `"Public Folders/LocalNews"`) {
		t.Errorf("alice missing her domain's LocalNews: %v", aliceList)
	}
	if hasLine(aliceList, "OtherNews") {
		t.Errorf("alice leaked other.test's OtherNews: %v", aliceList)
	}

	carol := dialLogin(t, addr, "carol@other.test", "secret")
	carolList := carol.mustOK("l", `LIST "" "*"`)
	if !hasLine(carolList, `"Public Folders/OtherNews"`) {
		t.Errorf("carol missing her domain's OtherNews: %v", carolList)
	}
	if hasLine(carolList, "LocalNews") {
		t.Errorf("carol leaked local.test's LocalNews: %v", carolList)
	}
}
