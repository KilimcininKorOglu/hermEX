package mta

import (
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/smtp"
)

// TestAnUnauthenticatedHostedSenderIsRefused proves an unauthenticated session
// cannot use an envelope sender in a hosted domain, as Exchange refuses an
// anonymous sender of an authoritative domain: only a forger sends that way, and
// a forged hosted sender is otherwise trusted downstream as itself (a meeting
// reply or cancellation). The null sender, a foreign sender, an authenticated
// owner and a split domain, whose other mail system sends as the domain, pass.
func TestAnUnauthenticatedHostedSenderIsRefused(t *testing.T) {
	hosted := directory.StaticAccounts{"bob@hosted.test": {Password: "pw", MailboxPath: t.TempDir()}}
	for _, from := range []string{"bob@hosted.test", "BOB@Hosted.Test", "nobody@hosted.test"} {
		err := (&session{accounts: hosted}).Mail(from, smtp.MailParams{})
		if err == nil || !strings.HasPrefix(err.Error(), "5.7.1") {
			t.Errorf("unauthenticated MAIL FROM:<%s> = %v, want a 5.7.1 refusal", from, err)
		}
	}
	for _, from := range []string{"", "someone@remote.example"} {
		if err := (&session{accounts: hosted}).Mail(from, smtp.MailParams{}); err != nil {
			t.Errorf("unauthenticated MAIL FROM:<%s> refused: %v", from, err)
		}
	}
	if err := (&session{accounts: hosted, authUser: "bob@hosted.test"}).Mail("bob@hosted.test", smtp.MailParams{}); err != nil {
		t.Errorf("the authenticated owner was refused: %v", err)
	}
	split := splitAccounts{directory.StaticAccounts{"alice@local": {Password: "pw", MailboxPath: t.TempDir()}}}
	if err := (&session{accounts: split}).Mail("colleague@local", smtp.MailParams{}); err != nil {
		t.Errorf("a sender of a split domain was refused: %v", err)
	}
}
