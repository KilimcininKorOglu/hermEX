package pop3

import (
	"bufio"
	"net"
	"net/textproto"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// privDir wraps a static directory but reports a fixed privilege set, so a test
// can deny a service to an otherwise-valid account. With unknown set it answers
// every privilege granted beside ok=false: a directory that cannot name the
// account, whose privileges must then grant nothing.
type privDir struct {
	directory.StaticAccounts
	privs   directory.ServicePrivileges
	unknown bool
}

func (d privDir) Privileges(user string) (directory.ServicePrivileges, bool) {
	if d.unknown {
		return directory.ServicePrivileges{POP3IMAP: true, SMTP: true, Web: true, EAS: true, DAV: true, ChgPasswd: true}, false
	}
	if _, ok := d.StaticAccounts[strings.ToLower(user)]; !ok {
		return directory.ServicePrivileges{}, false
	}
	return d.privs, true
}

// passReply provisions alice's mailbox, logs in as alice with the right password
// against a server on auth, and returns the server's reply to PASS.
func passReply(t *testing.T, auth privDir) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)
	auth.StaticAccounts = directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}

	ln := serveOn(t, &Server{Auth: auth, Hostname: "mail.test"})
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	r := textproto.NewReader(bufio.NewReader(conn))
	wantReply(t, r, "+OK") // greeting
	mustSend(t, conn, "USER alice\r\n")
	wantReply(t, r, "+OK")
	mustSend(t, conn, "PASS secret\r\n")
	l, err := r.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestPOP3PrivilegeDenied proves a user with valid credentials but no POP3/IMAP
// service privilege is refused at login, so disabling the privilege actually
// blocks access, rather than merely being stored.
func TestPOP3PrivilegeDenied(t *testing.T) {
	l := passReply(t, privDir{privs: directory.ServicePrivileges{POP3IMAP: false, SMTP: true, Web: true, EAS: true, DAV: true}})
	if !strings.HasPrefix(l, "-ERR") {
		t.Fatalf("PASS with a valid password but no POP3/IMAP privilege got %q, want -ERR", l)
	}
	if !strings.Contains(l, "disabled") {
		t.Errorf("denial message = %q, want it to mention the service is disabled", l)
	}
}

// TestPOP3RefusesAnAccountWithUnknownPrivileges proves the login gate fails
// closed: a directory that authenticates the password but cannot name the
// account's privileges (ok=false) grants none, whatever it returns beside that.
func TestPOP3RefusesAnAccountWithUnknownPrivileges(t *testing.T) {
	l := passReply(t, privDir{unknown: true})
	if !strings.HasPrefix(l, "-ERR") {
		t.Fatalf("PASS for an account with unknown privileges got %q, want -ERR", l)
	}
}
