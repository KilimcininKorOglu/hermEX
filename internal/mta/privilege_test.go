package mta

import (
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

// TestSMTPSubmissionPrivilege proves the SMTP service privilege gates
// authenticated submission only: a user with the privilege off cannot AUTH (so
// cannot submit), while inbound delivery to that same user is unaffected — the
// gate must never block a mailbox from receiving mail.
func TestSMTPSubmissionPrivilege(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bob")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	auth := privDir{
		StaticAccounts: directory.StaticAccounts{"bob@local": {Password: "pw", MailboxPath: dir}},
		privs:          directory.ServicePrivileges{SMTP: false, POP3IMAP: true, Web: true, EAS: true, DAV: true},
	}

	// Authenticated submission is refused: no SMTP privilege.
	if (&session{accounts: auth}).Auth("bob@local", "pw") {
		t.Error("AUTH succeeded for a user without the SMTP privilege, want failure")
	}

	// Inbound intake (no AUTH) for the same user is unaffected — a revoked SMTP
	// submission privilege must never stop a mailbox from receiving mail.
	if err := (&session{accounts: auth}).Rcpt("bob@local"); err != nil {
		t.Errorf("inbound Rcpt for an SMTP-disabled user refused: %v", err)
	}
}
