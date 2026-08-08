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

	done := make(chan error, 1)
	go func() {
		_, err := LookupMX("blackhole.invalid")
		done <- err
	}()

	select {
	case err := <-done:
		// Timing out is a resolution failure, the same answer any other failed
		// lookup gives, so the item is retried rather than lost.
		if err == nil {
			t.Error("an unanswerable domain resolved successfully")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LookupMX did not return, so one bad recipient domain stalls the drainer")
	}
}
