package imap

import (
	"bufio"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

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
	t.Cleanup(func() { ln.Close() })
	go (&Server{Auth: accounts, Hostname: "mail.test", Pub: pub}).Serve(ln)
	return ln.Addr().String()
}

// dialLogin dials the server and logs in, returning a ready client.
func dialLogin(t *testing.T, addr, user, pass string) *testClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")
	c.mustOK("login", "LOGIN "+user+" "+pass)
	return c
}

// doFull sends a command and returns the untagged lines and the full tagged
// completion line (so a test can inspect a response code like [READ-ONLY]).
func (c *testClient) doFull(tag, cmd string) (untagged []string, tagged string) {
	c.t.Helper()
	fmt.Fprintf(c.conn, "%s %s\r\n", tag, cmd)
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

	pub := publicfolder.New(pubPaths{root: root})
	if err := pub.Provision("local.test"); err != nil {
		t.Fatal(err)
	}
	ps, err := pub.OpenForDomain("local.test")
	if err != nil {
		t.Fatal(err)
	}
	ann, _ := ps.CreateFolder(nil, "Announcements")
	bul, _ := ps.CreateFolder(nil, "Bulletin")
	staff, _ := ps.CreateFolder(nil, "Staff")
	grantAnyone(t, ps, ann, mapi.FrightsVisible|mapi.FrightsReadAny)                                      // read-only for everyone
	grantUser(t, ps, bul, "alice@local.test", mapi.FrightsVisible|mapi.FrightsReadAny|mapi.FrightsCreate) // alice may post
	grantUser(t, ps, staff, "bob@local.test", mapi.FrightsVisible|mapi.FrightsReadAny)                    // bob only
	ps.Close()

	accounts := directory.StaticAccounts{"alice@local.test": {Password: "secret", MailboxPath: mbox}}
	addr := publicServer(t, accounts, pub)
	c := dialLogin(t, addr, "alice@local.test", "secret")

	// NAMESPACE advertises the public shared namespace.
	nsUn := c.mustOK("ns", "NAMESPACE")
	if !hasLine(nsUn, `("Public Folders/" "/")`) {
		t.Errorf("NAMESPACE missing the public shared namespace: %v", nsUn)
	}

	// LIST shows the folders alice may see (Announcements, Bulletin) and the
	// namespace container, but not Staff (granted only to bob).
	listUn := c.mustOK("l", `LIST "" "*"`)
	if !hasLine(listUn, `"Public Folders/Announcements"`) {
		t.Errorf("LIST missing Announcements: %v", listUn)
	}
	if !hasLine(listUn, `"Public Folders/Bulletin"`) {
		t.Errorf("LIST missing Bulletin: %v", listUn)
	}
	if hasLine(listUn, "Staff") {
		t.Errorf("LIST leaked Staff (alice has no grant): %v", listUn)
	}

	// A folder alice cannot see is not selectable.
	if _, status := c.do("s0", `SELECT "Public Folders/Staff"`); status != "NO" {
		t.Errorf("SELECT Staff = %s, want NO", status)
	}

	// Announcements: anyone-Reviewer → selectable, but read-only (no post rights).
	_, tagged := c.doFull("s1", `SELECT "Public Folders/Announcements"`)
	if !strings.Contains(tagged, "OK") || !strings.Contains(tagged, "[READ-ONLY]") {
		t.Errorf("SELECT Announcements = %q, want OK [READ-ONLY]", tagged)
	}
	if status := c.appendMsg("a1", `"Public Folders/Announcements"`, "Subject: x\r\n\r\nno"); status != "NO" {
		t.Errorf("APPEND to read-only Announcements = %s, want NO", status)
	}

	// Bulletin: alice has post rights → read-write, APPEND succeeds.
	if status := c.appendMsg("a2", `"Public Folders/Bulletin"`, "Subject: hi\r\n\r\nhello world"); status != "OK" {
		t.Fatalf("APPEND to Bulletin = %s, want OK", status)
	}
	selUn, tagged := c.doFull("s2", `SELECT "Public Folders/Bulletin"`)
	if !strings.Contains(tagged, "OK") || !strings.Contains(tagged, "[READ-WRITE]") {
		t.Errorf("SELECT Bulletin = %q, want OK [READ-WRITE]", tagged)
	}
	if !hasLine(selUn, "1 EXISTS") {
		t.Errorf("SELECT Bulletin should show 1 EXISTS after the post: %v", selUn)
	}
	// The posted message reads back from the public store (cross-store FETCH).
	fetchUn := c.mustOK("f", "FETCH 1 (BODY[TEXT])")
	if !hasLine(fetchUn, "hello world") {
		t.Errorf("FETCH from public folder did not return the body: %v", fetchUn)
	}
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
