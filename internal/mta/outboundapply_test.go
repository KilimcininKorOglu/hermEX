package mta

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
)

// limitedSend builds the pieces a relaying send needs: one local account, a real
// spool, and a limiter capped at n external recipients per hour.
func limitedSend(t *testing.T, n int) (directory.Accounts, *relay.Spool) {
	t.Helper()
	spool, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spool.Close() })

	l := NewOutboundLimiter()
	l.SetLimits(n, time.Hour)
	l.SetEnabled(true)
	SetOutboundLimiter(l)
	t.Cleanup(func() { SetOutboundLimiter(nil) })

	return directory.StaticAccounts{"alice@acme.test": {Password: "pw", MailboxPath: t.TempDir()}}, spool
}

// queued reports how many recipient entries are sitting in the spool.
func queued(t *testing.T, spool *relay.Spool) int {
	t.Helper()
	entries, err := spool.List()
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// TestDeliverAndRelayDefersOverCap proves the cap now applies to the shared send
// path, not only to SMTP submission: past the cap nothing is queued at all, so a
// compromised account cannot blast through webmail, EWS, ActiveSync or ROP.
func TestDeliverAndRelayDefersOverCap(t *testing.T) {
	accounts, spool := limitedSend(t, 2)
	raw := []byte("Subject: hi\r\n\r\nbody\r\n")

	recipients := []string{"a@far.test", "b@far.test", "c@far.test"}
	if _, err := DeliverAndRelay(accounts, spool, "alice@acme.test", recipients, raw, time.Unix(1000, 0)); !errors.Is(err, ErrOutboundLimited) {
		t.Fatalf("err = %v, want ErrOutboundLimited", err)
	}
	if n := queued(t, spool); n != 0 {
		t.Errorf("%d messages were queued despite the refusal", n)
	}
}

// TestDeliverAndRelayAdmitsUnderCap keeps ordinary sending working with the
// limiter on.
func TestDeliverAndRelayAdmitsUnderCap(t *testing.T) {
	accounts, spool := limitedSend(t, 5)
	raw := []byte("Subject: hi\r\n\r\nbody\r\n")

	if _, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"a@far.test", "b@far.test"}, raw, time.Unix(1000, 0)); err != nil {
		t.Fatalf("send under the cap failed: %v", err)
	}
	if n := queued(t, spool); n != 2 {
		t.Errorf("queued = %d, want one entry per recipient", n)
	}
}

// TestDeliverAndRelayCountsExternalOnly proves internal mail does not consume the
// window: the cap exists to catch outbound spam, and a busy internal thread must
// not exhaust it.
func TestDeliverAndRelayCountsExternalOnly(t *testing.T) {
	accounts, spool := limitedSend(t, 1)
	raw := []byte("Subject: hi\r\n\r\nbody\r\n")

	for range 3 {
		if _, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"alice@acme.test"}, raw, time.Unix(1000, 0)); err != nil {
			t.Fatalf("local send failed: %v", err)
		}
	}
	// The window is untouched, so one external recipient still gets through.
	if _, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"a@far.test"}, raw, time.Unix(1000, 0)); err != nil {
		t.Errorf("the external send was refused after local-only traffic: %v", err)
	}
}

// TestDeliverAndRelayWithoutLimiterIsUnchanged covers the daemons that install no
// limiter: the send path must behave exactly as before.
func TestDeliverAndRelayWithoutLimiterIsUnchanged(t *testing.T) {
	spool, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = spool.Close() }()
	accounts := directory.StaticAccounts{"alice@acme.test": {Password: "pw", MailboxPath: t.TempDir()}}

	many := make([]string, 50)
	for i := range many {
		many[i] = "r" + string(rune('a'+i%26)) + "@far.test"
	}
	if _, err := DeliverAndRelay(accounts, spool, "alice@acme.test", many, []byte("Subject: hi\r\n\r\nbody\r\n"), time.Unix(1000, 0)); err != nil {
		t.Fatalf("send with no limiter installed failed: %v", err)
	}
}

// TestApplyOutboundSettings covers the settings plumbing every daemon now shares:
// no stored row leaves the limiter alone, a stored row applies without a restart.
func TestApplyOutboundSettings(t *testing.T) {
	l := NewOutboundLimiter()
	ApplyOutboundSettings("test", nil, l, func() (OutboundSettings, bool, error) {
		return OutboundSettings{}, false, nil
	})
	if !l.Allow("alice@acme.test") {
		t.Error("an unsaved settings row enabled limiting")
	}

	ApplyOutboundSettings("test", nil, l, func() (OutboundSettings, bool, error) {
		return OutboundSettings{Enabled: true, RecipientCap: 1, WindowSeconds: 3600}, true, nil
	})
	if !l.Allow("bob@acme.test") {
		t.Fatal("the first recipient was refused under a cap of 1")
	}
	if l.Allow("bob@acme.test") {
		t.Error("the stored cap was not applied")
	}
}
