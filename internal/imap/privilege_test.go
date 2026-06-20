package imap

import (
	"bufio"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// privDir wraps a static directory but reports a fixed privilege set, so a test
// can deny a service to an otherwise-valid account.
type privDir struct {
	directory.StaticAccounts
	privs directory.ServicePrivileges
}

func (d privDir) Privileges(user string) (directory.ServicePrivileges, bool) {
	if _, ok := d.StaticAccounts[strings.ToLower(user)]; !ok {
		return directory.ServicePrivileges{}, false
	}
	return d.privs, true
}

// TestIMAPPrivilegeDenied proves a user with valid credentials but no POP3/IMAP
// service privilege is refused at LOGIN — so revoking the privilege actually
// blocks access, rather than merely being stored.
func TestIMAPPrivilegeDenied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	auth := privDir{
		StaticAccounts: directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}},
		privs:          directory.ServicePrivileges{POP3IMAP: false, SMTP: true, Web: true, EAS: true, DAV: true},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go (&Server{Auth: auth, Hostname: "mail.test"}).Serve(ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil { // greeting
		t.Fatal(err)
	}

	fmt.Fprintf(conn, "a LOGIN alice secret\r\n")
	resp, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp, "a NO") {
		t.Fatalf("LOGIN with a valid password but no POP3/IMAP privilege got %q, want a tagged NO", resp)
	}
	if !strings.Contains(resp, "disabled") {
		t.Errorf("denial response = %q, want it to mention the service is disabled", resp)
	}
}
