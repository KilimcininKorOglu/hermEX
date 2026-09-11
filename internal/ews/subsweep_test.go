package ews

import (
	"context"
	"testing"
	"time"

	"hermex/internal/directory"
)

// sweepServer returns a server with no listener; the sweep works on the registry
// alone.
func sweepServer() *Server {
	accs := directory.StaticAccounts{}
	return NewServer(accs, accs, "mail.hermex.test")
}

// addSub puts one subscription in the registry, last used idle ago.
func addSub(s *Server, id string, idle, timeout time.Duration, done chan struct{}) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	now := time.Now()
	s.subs[id] = &ewsSubscription{
		user:       testUser,
		mailbox:    "/tmp/nowhere",
		created:    now.Add(-idle),
		lastAccess: now.Add(-idle),
		timeout:    timeout,
		done:       done,
	}
}

// TestTheSweepDropsAnAbandonedSubscription is the leak. A pull or streaming
// subscription has no worker, so it used to be evicted only when a later request
// happened to name it: one left behind by a client that went away stayed for the
// life of the process, holding a snapshot as large as the mailbox.
func TestTheSweepDropsAnAbandonedSubscription(t *testing.T) {
	s := sweepServer()
	addSub(s, "expired", 31*time.Minute, 30*time.Minute, nil)
	addSub(s, "live", time.Minute, 30*time.Minute, nil)

	if n := s.SweepSubscriptions(time.Now()); n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if _, ok := s.subs["expired"]; ok {
		t.Error("the expired subscription survived the sweep")
	}
	if _, ok := s.subs["live"]; !ok {
		t.Error("the sweep dropped a subscription that is still within its lifetime")
	}
}

// TestTheSweepStopsAPushWorker keeps the sweep from leaving a worker running
// against an entry that is gone, which would keep POSTing to a callback for a
// subscription the server no longer holds.
func TestTheSweepStopsAPushWorker(t *testing.T) {
	s := sweepServer()
	done := make(chan struct{})
	addSub(s, "push", time.Hour, time.Minute, done)

	if n := s.SweepSubscriptions(time.Now()); n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}
	select {
	case <-done:
	default:
		t.Error("the push worker was not stopped")
	}
}

// TestTheSweepIsExactAtTheBoundary keeps a subscription for the whole idle period
// it was promised, because a client polling on its own deadline must not find it
// gone early.
func TestTheSweepIsExactAtTheBoundary(t *testing.T) {
	s := sweepServer()
	used := time.Now()
	s.subMu.Lock()
	s.subs["edge"] = &ewsSubscription{user: testUser, created: used, lastAccess: used, timeout: 30 * time.Minute}
	s.subMu.Unlock()

	if n := s.SweepSubscriptions(used.Add(30 * time.Minute)); n != 0 {
		t.Errorf("swept %d at exactly the deadline, want 0", n)
	}
	if n := s.SweepSubscriptions(used.Add(30*time.Minute + time.Nanosecond)); n != 1 {
		t.Errorf("swept %d one nanosecond past the deadline, want 1", n)
	}
}

// TestTheSweepKeepsASubscriptionInUse is the load-bearing case: the timeout is how
// long a subscription may stay IDLE, not how long it may live. A client that keeps
// polling must keep its subscription, whatever its age, or its sync stops with no
// notice.
func TestTheSweepKeepsASubscriptionInUse(t *testing.T) {
	s := sweepServer()
	now := time.Now()
	s.subMu.Lock()
	s.subs["busy"] = &ewsSubscription{
		user:       testUser,
		created:    now.Add(-8 * time.Hour), // subscribed long ago
		lastAccess: now.Add(-time.Minute),   // and used a minute ago
		timeout:    30 * time.Minute,
	}
	s.subMu.Unlock()

	if n := s.SweepSubscriptions(now); n != 0 {
		t.Errorf("swept %d, want 0: the subscription was used a minute ago", n)
	}
}

// TestTheSweepLoopStopsWithItsContext keeps the daemon's shutdown from leaving
// the goroutine running.
func TestTheSweepLoopStopsWithItsContext(t *testing.T) {
	s := sweepServer()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		s.RunSubscriptionSweep(ctx)
		close(stopped)
	}()
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Error("the sweep loop did not stop when its context was cancelled")
	}
}
