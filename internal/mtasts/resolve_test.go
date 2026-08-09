package mtasts

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestResolverLookup proves the cheap-TXT-then-HTTPS flow, max_age caching, and
// the skip-fetch-when-no-TXT shortcut.
func TestResolverLookup(t *testing.T) {
	const policyDoc = "version: STSv1\nmode: enforce\nmx: mail.example.com\nmax_age: 86400\n"
	var txtCalls, fetchCalls int
	now := time.Unix(1_700_000_000, 0)
	r := &Resolver{
		LookupTXT: func(name string) ([]string, error) {
			txtCalls++
			if name == "_mta-sts.example.com" {
				return []string{"v=STSv1; id=20260619T000000;"}, nil
			}
			return nil, &net.DNSError{IsNotFound: true}
		},
		FetchPolicy: func(string) (string, error) { fetchCalls++; return policyDoc, nil },
		Now:         func() time.Time { return now },
	}

	p, err := r.Lookup("example.com")
	if err != nil || p == nil || p.Mode != ModeEnforce {
		t.Fatalf("Lookup(policy domain) = %v, %v", p, err)
	}
	// Cached for max_age: a second lookup touches no network.
	if _, err := r.Lookup("example.com"); err != nil {
		t.Fatal(err)
	}
	if txtCalls != 1 || fetchCalls != 1 {
		t.Errorf("second lookup re-probed: txt=%d fetch=%d, want 1/1", txtCalls, fetchCalls)
	}

	// A domain without a TXT record yields no policy and never fetches.
	if p, err := r.Lookup("plain.example"); err != nil || p != nil {
		t.Errorf("Lookup(no policy) = %v, %v; want nil, nil", p, err)
	}
	if fetchCalls != 1 {
		t.Errorf("a domain with no TXT record still fetched: %d", fetchCalls)
	}

	// After max_age the policy is re-fetched.
	now = now.Add(86401 * time.Second)
	if _, err := r.Lookup("example.com"); err != nil {
		t.Fatal(err)
	}
	if fetchCalls != 2 {
		t.Errorf("expired policy not re-fetched: fetch=%d, want 2", fetchCalls)
	}
}

// TestResolverFetchError proves a fetch failure is surfaced as transient, not
// cached as "no policy", otherwise a blip would silently disable enforcement.
func TestResolverFetchError(t *testing.T) {
	r := &Resolver{
		LookupTXT:   func(string) ([]string, error) { return []string{"v=STSv1; id=x"}, nil },
		FetchPolicy: func(string) (string, error) { return "", errors.New("boom") },
	}
	if _, err := r.Lookup("example.com"); err == nil {
		t.Error("a fetch failure should be returned, not swallowed")
	}
	called := false
	r.FetchPolicy = func(string) (string, error) {
		called = true
		return "version: STSv1\nmode: enforce\nmx: a.example\nmax_age: 1\n", nil
	}
	if _, err := r.Lookup("example.com"); err != nil || !called {
		t.Errorf("after an error the policy should be re-fetched: called=%v err=%v", called, err)
	}
}

// TestPolicyClientRefusesInternalTarget proves the policy fetch cannot be steered
// into this network. The recipient's domain owner controls where mta-sts.<domain>
// resolves, so an unguarded client would dial whatever they point it at.
func TestPolicyClientRefusesInternalTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("reached"))
	}))
	defer srv.Close()

	resp, err := policyClient.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("the policy client reached a loopback address")
	}
}

// TestPolicyClientRefusesRedirects covers the second half of the guard, which is
// also what RFC 8461 §3.3 requires: a 3xx must not be followed. The dial guard
// blocks a loopback hop first, so the redirect policy is asserted on directly.
func TestPolicyClientRefusesRedirects(t *testing.T) {
	if policyClient.CheckRedirect == nil {
		t.Fatal("the policy client follows redirects")
	}
	if err := policyClient.CheckRedirect(nil, nil); err == nil {
		t.Error("CheckRedirect admitted a redirect")
	}
}

// TestDefaultTXTLookupHasADeadline proves the presence lookup gives up instead of
// waiting forever on a nameserver that accepts the query and never answers. The
// relay worker drains the outbound queue one item at a time, so an unbounded
// lookup here parks every other delivery behind one recipient domain.
func TestDefaultTXTLookupHasADeadline(t *testing.T) {
	hung := make(chan struct{})
	defer close(hung)

	prev := txtResolver
	txtResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// Accept the dial, then never speak: the resolver waits on the read.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-hung:
				return nil, errors.New("test over")
			}
		},
	}
	defer func() { txtResolver = prev }()

	start := time.Now()
	_, err := lookupTXTWithin(50*time.Millisecond, "_mta-sts.example.test")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a resolver that never answers returned success")
	}
	if elapsed > 2*time.Second {
		t.Errorf("the lookup took %s to give up; the deadline is not reaching it", elapsed)
	}
}

// TestLookupUsesTheDeadlineBoundDefault proves a zero-value Resolver, which is what
// the relay constructs, resolves through the deadline-bound path rather than the
// bare package-level lookup that has no deadline at all. Substituting the resolver
// is only visible if that path is the one taken.
func TestLookupUsesTheDeadlineBoundDefault(t *testing.T) {
	dialed := false
	prev := txtResolver
	txtResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("no nameserver in this test")
		},
	}
	defer func() { txtResolver = prev }()

	r := &Resolver{} // exactly what cmd/mta builds
	_, err := r.txtPresent("example.test")
	if !dialed {
		t.Fatal("the default lookup bypassed the deadline-bound resolver")
	}
	if err == nil {
		t.Error("the presence lookup reported success with no nameserver")
	}
}
