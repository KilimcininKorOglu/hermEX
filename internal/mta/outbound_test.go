package mta

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
	"hermex/internal/smtp"
)

// TestOutboundLimiterAdmitsUntilCapThenDefers proves an enabled limiter admits up to
// the cap of external recipients in a window and defers the rest, and that the next
// window admits again.
func TestOutboundLimiterAdmitsUntilCapThenDefers(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewOutboundLimiter()
	l.now = func() time.Time { return now }
	l.SetLimits(3, time.Hour)
	l.SetEnabled(true)

	for i := range 3 {
		if !l.Allow("alice@local") {
			t.Fatalf("recipient %d within the cap must be admitted", i+1)
		}
	}
	if l.Allow("alice@local") {
		t.Error("the recipient past the cap must be deferred")
	}
	now = now.Add(time.Hour)
	if !l.Allow("alice@local") {
		t.Error("a recipient in the next window must be admitted")
	}
}

// TestOutboundLimiterDisabledAdmitsEverything proves a disabled limiter never defers.
func TestOutboundLimiterDisabledAdmitsEverything(t *testing.T) {
	l := NewOutboundLimiter()
	l.SetLimits(1, time.Hour)
	for range 5 {
		if !l.Allow("alice@local") {
			t.Fatal("a disabled limiter must admit every recipient")
		}
	}
}

// TestOutboundLimiterKeysByAccount proves each account has its own budget — one
// account's blast does not throttle another.
func TestOutboundLimiterKeysByAccount(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewOutboundLimiter()
	l.now = func() time.Time { return now }
	l.SetLimits(1, time.Hour)
	l.SetEnabled(true)

	if !l.Allow("alice@local") {
		t.Fatal("alice's first recipient must be admitted")
	}
	if l.Allow("alice@local") {
		t.Fatal("alice's second recipient (over cap 1) must be deferred")
	}
	if !l.Allow("bob@local") {
		t.Error("a second account must have its own budget, not alice's exhausted one")
	}
}

// TestOutboundLimiterEmptyUserFailsOpen proves an empty account (no authenticated
// user) is admitted — the limiter never engages outside authenticated submission.
func TestOutboundLimiterEmptyUserFailsOpen(t *testing.T) {
	l := NewOutboundLimiter()
	l.SetLimits(1, time.Hour)
	l.SetEnabled(true)
	if !l.Allow("") {
		t.Error("an empty account must fail open (admit)")
	}
}

// TestOutboundLimiterAlertsOncePerWindow proves the over-cap alert fires exactly once
// per account per window — so a blast that trips the cap hundreds of times does not
// flood the log — and can fire again in a fresh window.
func TestOutboundLimiterAlertsOncePerWindow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var alerts []string
	l := NewOutboundLimiter()
	l.now = func() time.Time { return now }
	l.SetLimits(1, time.Hour)
	l.SetEnabled(true)
	l.SetAlerter(func(user string, _ int) { alerts = append(alerts, user) })

	l.Allow("alice@local") // count 1 = cap, admitted, no alert
	l.Allow("alice@local") // over cap → alert fires
	l.Allow("alice@local") // still over → no second alert
	if len(alerts) != 1 || alerts[0] != "alice@local" {
		t.Fatalf("alerts = %v, want exactly one for alice@local", alerts)
	}
	now = now.Add(time.Hour)
	l.Allow("alice@local") // new window, count 1 = cap
	l.Allow("alice@local") // over cap → alert fires again
	if len(alerts) != 2 {
		t.Errorf("alerts after a new window = %d, want 2 (one per window)", len(alerts))
	}
}

// outboundSession builds an authenticated submission session wired to a real relay
// spool and the given outbound limiter.
func outboundSession(t *testing.T, ob *OutboundLimiter, authUser string) *session {
	t.Helper()
	accounts := directory.StaticAccounts{"alice@local": {MailboxPath: filepath.Join(t.TempDir(), "alice")}}
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.Close() })
	return &session{accounts: accounts, spool: sp, authUser: authUser, outbound: ob}
}

// TestRcptOutboundDefersAuthenticatedBlast proves the relay hook defers an
// authenticated account's external recipients once it passes the cap.
func TestRcptOutboundDefersAuthenticatedBlast(t *testing.T) {
	ob := NewOutboundLimiter()
	ob.SetLimits(2, time.Hour)
	ob.SetEnabled(true)

	for i := range 2 {
		if err := outboundSession(t, ob, "alice@local").Rcpt("v" + strconv.Itoa(i) + "@remote"); err != nil {
			t.Fatalf("external recipient %d within the cap must be accepted, got %v", i+1, err)
		}
	}
	err := outboundSession(t, ob, "alice@local").Rcpt("v9@remote")
	if _, ok := errors.AsType[*smtp.TempError](err); !ok {
		t.Fatalf("the external recipient past the cap must defer with a TempError, got %v", err)
	}
}

// TestRcptOutboundSkipsUnauthenticated is the inverted-guard test: an unauthenticated
// session is never outbound-limited. It cannot relay externally at all — the address
// is refused as relay-denied (a plain error), never an outbound rate-limit TempError —
// so the limiter, which targets authenticated submission, never engages for inbound.
func TestRcptOutboundSkipsUnauthenticated(t *testing.T) {
	ob := NewOutboundLimiter()
	ob.SetLimits(1, time.Hour)
	ob.SetEnabled(true)

	err := outboundSession(t, ob, "").Rcpt("v@remote") // authUser == ""
	if err == nil {
		t.Fatal("unauthenticated relay must be refused")
	}
	if _, ok := errors.AsType[*smtp.TempError](err); ok {
		t.Error("unauthenticated relay must be refused as relay-denied, never an outbound rate-limit TempError")
	}
}
