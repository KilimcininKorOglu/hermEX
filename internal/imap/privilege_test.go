package imap

import (
	"bufio"
	"encoding/base64"
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
// service privilege is refused at LOGIN, so revoking the privilege actually
// blocks access, rather than merely being stored.
func TestIMAPPrivilegeDenied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)

	auth := privDir{
		StaticAccounts: directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}},
		privs:          directory.ServicePrivileges{POP3IMAP: false, SMTP: true, Web: true, EAS: true, DAV: true},
	}
	ln := serveOn(t, &Server{Auth: auth, Hostname: "mail.test"})

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	br := bufio.NewReader(conn)
	greeting, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix(t, "greeting", greeting, "* OK")

	resp := exchangeLine(t, conn, br, "a LOGIN alice secret")
	if !strings.HasPrefix(resp, "a NO") {
		t.Fatalf("LOGIN with a valid password but no POP3/IMAP privilege got %q, want a tagged NO", resp)
	}
	if !strings.Contains(resp, "disabled") {
		t.Errorf("denial response = %q, want it to mention the service is disabled", resp)
	}
	ir := base64.StdEncoding.EncodeToString([]byte("\x00alice\x00secret"))
	authResp := exchangeLine(t, conn, br, "a2 AUTHENTICATE PLAIN "+ir)
	if !strings.HasPrefix(authResp, "a2 NO") {
		t.Fatalf("AUTHENTICATE PLAIN with disabled privilege got %q, want tagged NO", authResp)
	}
	// The refusal leaves the session unauthenticated (RFC 3501 6.2.3: a NO to LOGIN
	// keeps the not-authenticated state), so the mailbox stays out of reach.
	resp = exchangeLine(t, conn, br, "b SELECT INBOX")
	if strings.HasPrefix(resp, "b OK") || strings.HasPrefix(resp, "* ") {
		t.Errorf("SELECT after the refused LOGIN = %q, want a refusal", resp)
	}
}

// exchangeLine sends one command line and returns the first reply line, failing
// the test when either the write or the read fails.
func exchangeLine(t *testing.T, conn net.Conn, br *bufio.Reader, cmd string) string {
	t.Helper()
	if n, err := fmt.Fprintf(conn, "%s\r\n", cmd); err != nil || n == 0 {
		t.Fatalf("send %q (n=%d): %v", cmd, n, err)
	}
	resp, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read the reply to %q: %v", cmd, err)
	}
	return resp
}
