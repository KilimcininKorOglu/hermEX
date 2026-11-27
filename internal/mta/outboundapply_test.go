package mta

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/relay"
)

// limitedSend builds the pieces a relaying send needs: one local account, a real
// spool, and a limiter capped at n external recipients per hour.
func limitedSend(t *testing.T, n int) (directory.StaticAccounts, *relay.Spool) {
	t.Helper()
	spool, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustClose(t, spool) })

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
// path, not only to SMTP submission: past the cap nothing is delivered or queued
// at all, so a compromised account cannot blast through webmail, EWS, ActiveSync or ROP.
func TestDeliverAndRelayDefersOverCap(t *testing.T) {
	accounts, spool := limitedSend(t, 2)
	raw := []byte("Subject: hi\r\n\r\nbody\r\n")

	recipients := []string{"alice@acme.test", "a@far.test", "b@far.test", "c@far.test"}
	unresolved, err := DeliverAndRelay(accounts, spool, "alice@acme.test", recipients, raw, time.Unix(1000, 0))
	if !errors.Is(err, ErrOutboundLimited) {
		t.Fatalf("err = %v, want ErrOutboundLimited", err)
	}
	wantEq(t, "unresolved on outbound refusal", len(unresolved), len(recipients))
	var terminal interface{ TerminalDelivery() bool }
	if errors.As(err, &terminal) && terminal.TerminalDelivery() {
		t.Error("ErrOutboundLimited must be temporary (not terminal)")
	}
	if n := inboxCount(t, accounts["alice@acme.test"].MailboxPath); n != 0 {
		t.Errorf("%d local messages delivered despite refusal", n)
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

	unresolved, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"a@far.test", "b@far.test"}, raw, time.Unix(1000, 0))
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("send under the cap failed: unresolved=%v err=%v", unresolved, err)
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
		unresolved, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"alice@acme.test"}, raw, time.Unix(1000, 0))
		if err != nil || len(unresolved) != 0 {
			t.Fatalf("local send failed: unresolved=%v err=%v", unresolved, err)
		}
	}
	// The window is untouched, so one external recipient still gets through.
	unresolved, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"a@far.test"}, raw, time.Unix(1000, 0))
	if err != nil || len(unresolved) != 0 {
		t.Errorf("the external send was refused after local-only traffic: unresolved=%v err=%v", unresolved, err)
	}
}

// TestDeliverAndRelayWithoutLimiterIsUnchanged covers the daemons that install no
// limiter: the send path must behave exactly as before.
func TestDeliverAndRelayWithoutLimiterIsUnchanged(t *testing.T) {
	spool, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, spool)
	accounts := directory.StaticAccounts{"alice@acme.test": {Password: "pw", MailboxPath: t.TempDir()}}

	many := make([]string, 50)
	for i := range many {
		many[i] = "r" + string(rune('a'+i%26)) + "@far.test"
	}
	unresolved, err := DeliverAndRelay(accounts, spool, "alice@acme.test", many, []byte("Subject: hi\r\n\r\nbody\r\n"), time.Unix(1000, 0))
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("send with no limiter installed failed: unresolved=%v err=%v", unresolved, err)
	}
}

// TestApplyOutboundSettings covers the settings plumbing every daemon now shares:
// no stored row leaves the limiter alone, a stored row applies without a restart,
// and a read error keeps the running limit unchanged while logging the failure.
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

	sink := &captureSink{}
	ApplyOutboundSettings("test", logging.New(sink), l, func() (OutboundSettings, bool, error) {
		return OutboundSettings{Enabled: false, RecipientCap: 999}, true, errors.New("db down")
	})
	if l.Allow("bob@acme.test") {
		t.Error("a read error altered the active outbound cap")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if _, ok := findEvent(sink.events, "settings.read.fail"); !ok {
		t.Error("outbound settings read failure was not logged")
	}
}
