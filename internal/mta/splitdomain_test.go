package mta

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
	"hermex/internal/smtp"
)

// splitAccounts is a directory whose "local" domain is split with another host.
type splitAccounts struct{ directory.StaticAccounts }

func (splitAccounts) SplitRelayHost(domain string) (string, error) {
	if strings.EqualFold(domain, "local") {
		return "legacy.local", nil
	}
	return "", nil
}

// TestSplitDomainRelaysUnknownLocal proves an authenticated sender's mail to an
// address of a split domain with no mailbox here is relayed, while postmaster of
// that domain and every unauthenticated intake stay refused, so the other host
// cannot loop mail back through this server.
func TestSplitDomainRelaysUnknownLocal(t *testing.T) {
	accounts := splitAccounts{directory.StaticAccounts{"alice@local": {Password: "pw", MailboxPath: filepath.Join(t.TempDir(), "alice")}}}
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	mustNoErr(t, "open the relay spool", err)
	defer sp.Close()

	s := &session{accounts: accounts, spool: sp, authUser: "alice@local"}
	mustNoErr(t, "RCPT to an address on the other host", s.Rcpt("ghost@local", smtp.RcptParams{}))
	if err := s.Rcpt("postmaster@local", smtp.RcptParams{}); err == nil {
		t.Error("postmaster of a split domain was relayed")
	}
	wantEq(t, "relay targets", len(s.relayTargets), 1)

	u := &session{accounts: accounts, spool: sp}
	if err := u.Rcpt("ghost@local", smtp.RcptParams{}); err == nil {
		t.Error("unauthenticated intake for a split address was accepted")
	}

	unresolved, err := DeliverAndRelay(accounts, sp, "alice@local", []string{"ghost2@local"},
		[]byte("Subject: hi\r\n\r\nhello\r\n"), time.Now())
	mustNoErr(t, "deliver and relay", err)
	wantEq(t, "unresolved after a split relay", len(unresolved), 0)
}
