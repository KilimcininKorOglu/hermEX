package mta

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"hermex/internal/smtp"
)

// TestRateLimiterAdmitsUntilBurstThenDefers proves an enabled limiter admits up to
// the burst within a window and defers the rest, and that the next window admits
// again, so a legitimate sender that retries after the window eventually succeeds
// (the defer is temporary, not a permanent rejection).
func TestRateLimiterAdmitsUntilBurstThenDefers(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rl := NewRateLimiter()
	rl.now = func() time.Time { return now }
	rl.SetLimits(3, time.Minute)
	rl.SetLimits(0, -time.Second) // non-positive values ignored
	rl.SetEnabled(true)

	ip := net.ParseIP("198.51.100.7")
	for i := range 3 {
		if !rl.Allow(ip) {
			t.Fatalf("message %d within the burst must be admitted", i+1)
		}
	}
	if rl.Allow(ip) {
		t.Error("the message past the burst must be deferred")
	}
	// Next window: the counter resets and the sender is admitted again.
	now = now.Add(time.Minute)
	if !rl.Allow(ip) {
		t.Error("a message in the next window must be admitted")
	}
}

// TestRateLimiterDisabledAdmitsEverything proves a disabled limiter never defers, so
// rate limiting stays inert until an admin enables it.
func TestRateLimiterDisabledAdmitsEverything(t *testing.T) {
	rl := NewRateLimiter()
	rl.SetLimits(1, time.Minute)
	ip := net.ParseIP("198.51.100.7")
	for range 5 {
		if !rl.Allow(ip) {
			t.Fatal("a disabled limiter must admit every message")
		}
	}
}

// TestRateLimiterKeysByNetwork proves two addresses in the same /24 share one counter
// , a spammer rotating addresses within a pool cannot multiply its budget, while a
// different /24 keeps its own budget.
func TestRateLimiterKeysByNetwork(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rl := NewRateLimiter()
	rl.now = func() time.Time { return now }
	rl.SetLimits(2, time.Minute)
	rl.SetEnabled(true)

	if !rl.Allow(net.ParseIP("203.0.113.1")) {
		t.Fatal("the 1st message must be admitted")
	}
	if !rl.Allow(net.ParseIP("203.0.113.2")) {
		t.Fatal("the 2nd message from the same /24 must be admitted (still within burst)")
	}
	if rl.Allow(net.ParseIP("203.0.113.250")) {
		t.Error("the 3rd message from the same /24 must be deferred, the pool shares one budget")
	}
	if !rl.Allow(net.ParseIP("203.0.114.1")) {
		t.Error("a different /24 must have its own budget")
	}
	if !rl.Allow(net.ParseIP("2001:db8:1::1")) || !rl.Allow(net.ParseIP("2001:db8:1::2")) {
		t.Fatal("first two IPv6 messages in same /64 must be admitted")
	}
	if rl.Allow(net.ParseIP("2001:db8:1::99")) {
		t.Error("third IPv6 message in same /64 must be deferred")
	}
}

// TestRateLimiterNilIPFailsOpen proves an unkeyable client is admitted rather than
// blocked, the limiter never loses mail on its own inability to key the sender.
func TestRateLimiterNilIPFailsOpen(t *testing.T) {
	rl := NewRateLimiter()
	rl.SetLimits(1, time.Minute)
	rl.SetEnabled(true)
	if !rl.Allow(nil) {
		t.Error("a nil IP must fail open (admit)")
	}
}

// TestMailRateLimitDefersUnauthenticatedFlood proves the delivery hook defers an
// unauthenticated sender once its network passes the burst, returning a TempError so
// the SMTP server replies 451 (temporary), the flood is slowed but a legitimate
// server's retry eventually gets through.
func TestMailRateLimitDefersUnauthenticatedFlood(t *testing.T) {
	rl := NewRateLimiter()
	rl.SetLimits(2, time.Minute)
	rl.SetEnabled(true)

	newUnauth := func() *session { return &session{remoteAddr: "203.0.113.9:2500", rateLimit: rl} }
	for i := range 2 {
		if err := newUnauth().Mail("spammer@ext.example", smtp.MailParams{}); err != nil {
			t.Fatalf("message %d within the burst must be accepted, got %v", i+1, err)
		}
	}
	sOver := newUnauth()
	err := sOver.Mail("spammer@ext.example", smtp.MailParams{})
	if _, ok := errors.AsType[*smtp.TempError](err); !ok {
		t.Fatalf("the message past the burst must defer with a TempError, got %v", err)
	}
	wantEq(t, "session from after rate-limit deferral", sOver.from, "")
}

// TestMailRateLimitSkipsAuthenticated proves authenticated submission is never rate
// limited, even from a network whose unauthenticated budget is already spent.
func TestMailRateLimitSkipsAuthenticated(t *testing.T) {
	rl := NewRateLimiter()
	rl.SetLimits(1, time.Minute)
	rl.SetEnabled(true)
	accounts := resolveOnly{"alice@test": "/x"}

	// Spend the network's unauthenticated budget (burst 1).
	if err := (&session{accounts: accounts, remoteAddr: "203.0.113.9:2500", rateLimit: rl}).Mail("x@ext.example", smtp.MailParams{}); err != nil {
		t.Fatalf("the first unauthenticated message is accepted, got %v", err)
	}
	// An authenticated submission from the same network is still accepted.
	s := &session{accounts: accounts, authUser: "alice@test", remoteAddr: "203.0.113.9:2500", rateLimit: rl}
	if err := s.Mail("alice@test", smtp.MailParams{}); err != nil {
		t.Errorf("authenticated submission must never be rate limited, got %v", err)
	}
}

// TestRateLimiterFullTableStillLimitsANewcomer fills the table with live windows
// from distinct IPv6 /64s, which one /48 holds 65536 of, and then floods from a
// new network. A full table that admitted the newcomer untracked would switch
// the limit off for everyone; instead the network idle longest is evicted, as
// nginx limit_req does with a full zone, so the flood is deferred past its burst.
func TestRateLimiterFullTableStillLimitsANewcomer(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rl := NewRateLimiter()
	rl.now = func() time.Time { return now }
	rl.SetLimits(1, time.Hour)
	rl.SetEnabled(true)
	for i := range rateLimitMaxKeys {
		ip := net.ParseIP(fmt.Sprintf("2001:db8:%x:%x::1", i>>16, i&0xffff))
		if !rl.Allow(ip) {
			t.Fatalf("seed IP %d refused", i)
		}
	}

	flood := net.ParseIP("198.51.100.7")
	if !rl.Allow(flood) {
		t.Fatal("the newcomer's first message was deferred, want it admitted")
	}
	if rl.Allow(flood) {
		t.Fatal("the newcomer's second message was admitted, want it tracked and deferred past the burst")
	}
	if got := len(rl.windows); got != rateLimitMaxKeys {
		t.Errorf("table holds %d windows, want it kept at %d", got, rateLimitMaxKeys)
	}
}

// TestRateLimiterPruneDropsExpired proves Prune removes elapsed windows so the table
// stays bounded over time.
func TestRateLimiterPruneDropsExpired(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rl := NewRateLimiter()
	rl.now = func() time.Time { return now }
	rl.SetLimits(1, time.Minute)
	rl.SetEnabled(true)
	if !rl.Allow(net.ParseIP("203.0.113.1")) {
		t.Fatal("first message refused")
	}
	if len(rl.windows) != 1 {
		t.Fatalf("expected 1 tracked window, got %d", len(rl.windows))
	}
	now = now.Add(2 * time.Minute)
	rl.Prune()
	if len(rl.windows) != 0 {
		t.Errorf("Prune must drop the expired window; %d remain", len(rl.windows))
	}
}
