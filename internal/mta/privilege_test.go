package mta

import (
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/smtp"
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

// unknownPrivDir authenticates the password but reports the user unknown to
// Privileges while returning SMTP: true alongside known=false.
type unknownPrivDir struct{ directory.StaticAccounts }

func (unknownPrivDir) Privileges(string) (directory.ServicePrivileges, bool) {
	return directory.ServicePrivileges{SMTP: true, POP3IMAP: true, Web: true, EAS: true, DAV: true}, false
}

// TestSMTPSubmissionPrivilege proves the SMTP service privilege gates
// authenticated submission only: a user with the privilege off cannot AUTH (so
// cannot submit), while inbound delivery to that same user is unaffected, the
// gate must never block a mailbox from receiving mail.
func TestSMTPSubmissionPrivilege(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bob")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)

	auth := privDir{
		StaticAccounts: directory.StaticAccounts{"bob@local": {Password: "pw", MailboxPath: dir}},
		privs:          directory.ServicePrivileges{SMTP: false, POP3IMAP: true, Web: true, EAS: true, DAV: true},
	}

	// Authenticated submission is refused: no SMTP privilege.
	s := &session{accounts: auth}
	if s.Auth("bob@local", "pw") {
		t.Error("AUTH succeeded for a user without the SMTP privilege, want failure")
	}
	if s.authUser != "" || s.authMailbox != "" {
		t.Errorf("refused AUTH mutated session: user=%q mailbox=%q", s.authUser, s.authMailbox)
	}

	// Non-Authenticator directory refuses AUTH without mutating session.
	sNoAuth := &session{accounts: resolveOnly{"bob@local": dir}}
	if sNoAuth.Auth("bob@local", "pw") || sNoAuth.authUser != "" {
		t.Error("non-Authenticator directory accepted AUTH")
	}

	// Inbound intake (no AUTH) for the same user is unaffected, a revoked SMTP
	// submission privilege must never stop a mailbox from receiving mail.
	if err := (&session{accounts: auth}).Rcpt("bob@local", smtp.RcptParams{}); err != nil {
		t.Errorf("inbound Rcpt for an SMTP-disabled user refused: %v", err)
	}
}

// TestSMTPAuthRefusesUnknownPrivileges proves the service gate fails closed
// when Privileges returns ok=false even if SMTP is true in the struct.
func TestSMTPAuthRefusesUnknownPrivileges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bob")
	accs := unknownPrivDir{directory.StaticAccounts{"bob@local": {Password: "pw", MailboxPath: dir}}}
	s := &session{accounts: accs}
	if s.Auth("bob@local", "pw") {
		t.Fatal("AUTH succeeded when Privileges returned ok=false")
	}
	if s.authUser != "" || s.authMailbox != "" {
		t.Errorf("refused AUTH mutated session: user=%q mailbox=%q", s.authUser, s.authMailbox)
	}
}
