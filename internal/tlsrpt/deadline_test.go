package tlsrpt

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// TestAZeroResolverGivesUpOnASilentNameserver looks up a policy through the
// zero-value Resolver the relay builds, against a nameserver that accepts the
// query and never answers. The relay runs the daily report pass on its drain
// tick, so a lookup without a deadline would stop every outbound delivery.
func TestAZeroResolverGivesUpOnASilentNameserver(t *testing.T) {
	hung := make(chan struct{})
	defer close(hung)
	prevResolver, prevTimeout := txtResolver, lookupTimeout
	txtResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-hung:
				return nil, errors.New("test over")
			}
		},
	}
	lookupTimeout = 50 * time.Millisecond
	defer func() { txtResolver, lookupTimeout = prevResolver, prevTimeout }()

	done := make(chan error, 1)
	go func() {
		_, err := (&Resolver{}).Lookup("example.test")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a nameserver that never answers produced a policy lookup without error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the lookup was still waiting after 2s; no deadline reaches it")
	}
}
