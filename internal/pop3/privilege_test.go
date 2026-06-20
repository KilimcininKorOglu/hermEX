package pop3

import (
	"bufio"
	"fmt"
	"net"
	"net/textproto"
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

// TestPOP3PrivilegeDenied proves a user with valid credentials but no POP3/IMAP
// service privilege is refused at login — so disabling the privilege actually
// blocks access, rather than merely being stored.
func TestPOP3PrivilegeDenied(t *testing.T) {
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
	r := textproto.NewReader(bufio.NewReader(conn))

	mustOK := func() {
		t.Helper()
		l, err := r.ReadLine()
		if err != nil || !strings.HasPrefix(l, "+OK") {
			t.Fatalf("want +OK, got %q (err %v)", l, err)
		}
	}
	mustOK() // greeting
	fmt.Fprintf(conn, "USER alice\r\n")
	mustOK()
	fmt.Fprintf(conn, "PASS secret\r\n")
	l, err := r.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(l, "-ERR") {
		t.Fatalf("PASS with a valid password but no POP3/IMAP privilege got %q, want -ERR", l)
	}
	if !strings.Contains(l, "disabled") {
		t.Errorf("denial message = %q, want it to mention the service is disabled", l)
	}
}
