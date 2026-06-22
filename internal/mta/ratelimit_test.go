package mta

import (
	"net"
	"testing"
	"time"
)

// TestRateLimiterAdmitsUntilBurstThenDefers proves an enabled limiter admits up to
// the burst within a window and defers the rest, and that the next window admits
// again — so a legitimate sender that retries after the window eventually succeeds
// (the defer is temporary, not a permanent rejection).
func TestRateLimiterAdmitsUntilBurstThenDefers(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rl := NewRateLimiter()
	rl.now = func() time.Time { return now }
	rl.SetLimits(3, time.Minute)
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
// — a spammer rotating addresses within a pool cannot multiply its budget — while a
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
		t.Error("the 3rd message from the same /24 must be deferred — the pool shares one budget")
	}
	if !rl.Allow(net.ParseIP("203.0.114.1")) {
		t.Error("a different /24 must have its own budget")
	}
}

// TestRateLimiterNilIPFailsOpen proves an unkeyable client is admitted rather than
// blocked — the limiter never loses mail on its own inability to key the sender.
func TestRateLimiterNilIPFailsOpen(t *testing.T) {
	rl := NewRateLimiter()
	rl.SetLimits(1, time.Minute)
	rl.SetEnabled(true)
	if !rl.Allow(nil) {
		t.Error("a nil IP must fail open (admit)")
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
	rl.Allow(net.ParseIP("203.0.113.1"))
	if len(rl.windows) != 1 {
		t.Fatalf("expected 1 tracked window, got %d", len(rl.windows))
	}
	now = now.Add(2 * time.Minute)
	rl.Prune()
	if len(rl.windows) != 0 {
		t.Errorf("Prune must drop the expired window; %d remain", len(rl.windows))
	}
}
