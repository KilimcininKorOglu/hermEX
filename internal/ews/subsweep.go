package ews

import (
	"context"
	"time"
)

// subSweepInterval is how often expired subscriptions are dropped. It matches the
// cadence of the other maintenance loops, so an entry outlives its timeout by at
// most a minute.
const subSweepInterval = time.Minute

// SweepSubscriptions drops every subscription whose lifetime has passed and
// returns how many it removed.
//
// A pull or streaming subscription has no worker of its own: it was evicted only
// when a later request happened to name it, so one created by a client that then
// went away stayed in the registry for the life of the process. Every entry
// holds a snapshot of the mailbox's message ids, which is as large as the
// mailbox, and a reconnecting client subscribes again on every reconnect. A push
// subscription is dropped by its own worker on the same deadline, so it is
// already gone by the time this runs and passing over it costs nothing.
func (s *Server) SweepSubscriptions(now time.Time) int {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	removed := 0
	for id, sub := range s.subs {
		if now.Sub(sub.created) <= sub.timeout {
			continue
		}
		delete(s.subs, id)
		// A push subscription's worker is stopped the way Unsubscribe stops it, so
		// the sweep cannot leave one running against an entry that is gone.
		if sub.done != nil {
			close(sub.done)
		}
		removed++
	}
	return removed
}

// RunSubscriptionSweep drops expired subscriptions every minute until ctx is
// cancelled. The daemon starts it; a server without it keeps the previous
// behaviour, which is that a subscription is evicted only when a request names it.
func (s *Server) RunSubscriptionSweep(ctx context.Context) {
	t := time.NewTicker(subSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SweepSubscriptions(time.Now())
		}
	}
}
