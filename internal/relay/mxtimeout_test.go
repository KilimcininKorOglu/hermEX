package relay

import (
	"context"
	"net"
	"testing"
	"time"
)

// hangingResolver answers nothing at all, the shape of a nameserver a recipient
// domain's owner simply points into a hole. Every dial blocks until the lookup's
// own deadline cancels it, so a lookup with no deadline never returns.
func hangingResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
}

// TestLookupMXIsBounded proves an unanswerable recipient domain cannot stall the
// spool. Exactly one process drains at a time and this lookup starts every
// delivery attempt, so an unbounded one holds every other queued message behind a
// single bad domain until the OS resolver gives up.
func TestLookupMXIsBounded(t *testing.T) {
	oldResolver, oldTimeout := mxResolver, mxLookupTimeout
	mxResolver, mxLookupTimeout = hangingResolver(), 200*time.Millisecond
	t.Cleanup(func() { mxResolver, mxLookupTimeout = oldResolver, oldTimeout })

	type answer struct {
		hosts []string
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		hosts, err := LookupMX("blackhole.invalid")
		done <- answer{hosts, err}
	}()

	select {
	case got := <-done:
		// Timing out is a resolution failure, the same answer any other failed
		// lookup gives, so the item is retried rather than lost.
		if got.err == nil || len(got.hosts) != 0 {
			t.Errorf("an unanswerable domain resolved to %v (err %v), want an error and no host", got.hosts, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LookupMX did not return, so one bad recipient domain stalls the drainer")
	}
}
