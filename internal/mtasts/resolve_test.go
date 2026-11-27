package mtasts

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	mustNoErr(t, err, "Lookup(policy domain)")
	if p == nil {
		t.Fatal("Lookup(policy domain) returned no policy")
	}
	wantEq(t, p.Mode, ModeEnforce, "the policy mode")

	// Cached for max_age: a second lookup touches no network.
	_, err = r.Lookup("example.com")
	mustNoErr(t, err, "the second lookup")
	wantEq(t, txtCalls, 1, "the TXT probes after a cached lookup")
	wantEq(t, fetchCalls, 1, "the policy fetches after a cached lookup")

	// A domain without a TXT record yields no policy and never fetches.
	plain, err := r.Lookup("plain.example")
	mustNoErr(t, err, "Lookup(no policy)")
	wantNoPolicy(t, plain, "the policy of a domain with no TXT record")
	wantEq(t, fetchCalls, 1, "the policy fetches after a domain with no TXT record")

	// After max_age the policy is re-fetched.
	now = now.Add(86401 * time.Second)
	_, err = r.Lookup("example.com")
	mustNoErr(t, err, "the lookup after max_age")
	wantEq(t, fetchCalls, 2, "the policy fetches once max_age expired")
}

// TestResolverFetchError proves a fetch failure is surfaced as transient, not
// cached as "no policy", otherwise a blip would silently disable enforcement.
func TestResolverFetchError(t *testing.T) {
	r := &Resolver{
		LookupTXT:   func(string) ([]string, error) { return []string{"v=STSv1; id=x"}, nil },
		FetchPolicy: func(string) (string, error) { return "", errors.New("boom") },
	}
	if got, err := r.Lookup("example.com"); err == nil || got != nil {
		t.Errorf("a fetch failure should return (nil, err), got (%+v, %v)", got, err)
	}
	r.mu.Lock()
	cacheLen := len(r.cache)
	r.mu.Unlock()
	wantEq(t, cacheLen, 0, "cache entries after transient fetch failure")
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
	var reached atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		if _, err := w.Write([]byte("reached")); err != nil {
			t.Errorf("write loopback response: %v", err)
		}
	}))
	defer srv.Close()

	resp, err := policyClient.Get(srv.URL)
	if err == nil {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close unexpected response body: %v", cerr)
		}
		t.Fatal("the policy client reached a loopback address")
	}
	wantEq(t, reached.Load(), false, "loopback handler reached")
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

// TestAnOversizedPolicyIsNotFetched serves a policy whose mx lines run past the
// 64 KB bound RFC 8461 3.3 suggests. Cutting the body at the bound would parse
// a prefix the domain never published, possibly ending inside an mx name; the
// RFC instead treats a policy that cannot be fetched "for any reason" as no
// policy. A body exactly at the bound still reads whole.
func TestAnOversizedPolicyIsNotFetched(t *testing.T) {
	head := "version: STSv1\nmode: enforce\nmax_age: 86400\n"
	pad := func(n int) string {
		body := head
		for len(body) < n {
			body += "mx: mx.example.com\n"
		}
		return body[:n]
	}
	serve := func(body string) *http.Response {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "text/plain")
		rec.WriteHeader(http.StatusOK)
		if _, err := rec.WriteString(body); err != nil {
			t.Fatalf("write test body: %v", err)
		}
		return rec.Result()
	}
	if got, err := readPolicyBody(serve(pad(maxPolicyBytes+1)), "example.com"); err == nil {
		t.Fatalf("a %d-byte policy was read as %d bytes", maxPolicyBytes+1, len(got))
	}
	at := pad(maxPolicyBytes)
	if got, err := readPolicyBody(serve(at), "example.com"); err != nil || got != at {
		t.Fatalf("a policy at the bound: err = %v, read %d of %d bytes", err, len(got), len(at))
	}
}

// TestAPolicyMustBeServedAsTextPlain serves the same valid policy under several
// media types. RFC 8461 3.2 has the sender check for "text/plain", so a host that
// lets users publish HTML or other files at the well-known path cannot have one
// of those read as the domain's policy; the type compares without regard to case
// and its parameters are ignored.
func TestAPolicyMustBeServedAsTextPlain(t *testing.T) {
	const doc = "version: STSv1\nmode: enforce\nmx: mx.example.com\nmax_age: 86400\n"
	serve := func(ctype string) *http.Response {
		rec := httptest.NewRecorder()
		if ctype != "" {
			rec.Header().Set("Content-Type", ctype)
		}
		rec.WriteHeader(http.StatusOK)
		if _, err := rec.WriteString(doc); err != nil {
			t.Fatalf("write test doc: %v", err)
		}
		res := rec.Result()
		if ctype == "" {
			res.Header.Del("Content-Type")
		}
		return res
	}
	for _, ok := range []string{"text/plain", "TEXT/Plain; charset=utf-8", "text/plain;charset=us-ascii"} {
		if _, err := readPolicyBody(serve(ok), "example.com"); err != nil {
			t.Errorf("Content-Type %q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"text/html", "application/octet-stream", "", "text/plainx"} {
		if _, err := readPolicyBody(serve(bad), "example.com"); err == nil {
			t.Errorf("Content-Type %q was read as a policy", bad)
		}
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

// TestResolverEvictsExpiredEntries proves the cache does not grow without bound: an
// entry for a domain that is never looked up again is deleted once it expires, the
// next time any miss stores a new entry. Without eviction the map would retain one
// entry per distinct domain ever queried for the process's whole life.
func TestResolverEvictsExpiredEntries(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	r := &Resolver{
		Now:       func() time.Time { return clock },
		LookupTXT: func(string) ([]string, error) { return nil, nil }, // no policy: negative cache
	}
	if _, err := r.Lookup("oneoff.example"); err != nil {
		t.Fatal(err)
	}
	// The one-off domain is now negatively cached; let it expire.
	clock = clock.Add(negativeTTL + time.Minute)
	// A miss for a different domain stores a new entry and sweeps the expired one.
	if _, err := r.Lookup("other.example"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	_, stillThere := r.cache["oneoff.example"]
	size := len(r.cache)
	r.mu.Unlock()
	if stillThere {
		t.Error("expired one-off entry was not evicted; the cache grows without bound")
	}
	if size != 1 {
		t.Errorf("cache holds %d entries, want only the live one", size)
	}
}

// TestReadPolicyBodyRejectsNon200 verifies RFC 8461 3.3: only HTTP 200 is a
// policy; 3xx redirects, 4xx, and 5xx responses are refused.
func TestReadPolicyBodyRejectsNon200(t *testing.T) {
	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusNotFound, http.StatusInternalServerError} {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "text/plain")
		rec.WriteHeader(code)
		if _, err := rec.WriteString("version: STSv1\nmode: enforce\nmx: mx.example\nmax_age: 60\n"); err != nil {
			t.Fatalf("write status %d body: %v", code, err)
		}
		if got, err := readPolicyBody(rec.Result(), "example.com"); err == nil || got != "" {
			t.Errorf("HTTP %d: got (%q, %v), want empty error", code, got, err)
		}
	}
}

// TestResolverRefusesHostileResponsesWithoutCaching verifies RFC 8461 3.1/3.3
// refusal paths: DNS SERVFAIL, invalid policy syntax, and forged non-STSv1 TXT
// records. Refusals must not alter existing cache state.
func TestResolverRefusesHostileResponsesWithoutCaching(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var fetchedBadTXT bool
	r := newHostileResolver(now, &fetchedBadTXT)

	mustNoErr(t, func() error { _, err := r.Lookup("good.example"); return err }(), "seed good.example")
	for _, domain := range []string{"servfail.example", "badpolicy.example"} {
		got, err := r.Lookup(domain)
		if err == nil {
			t.Errorf("Lookup(%s): expected error, got nil", domain)
		}
		wantNoPolicy(t, got, "policy for "+domain)
		r.mu.Lock()
		_, present := r.cache[domain]
		_, goodStill := r.cache["good.example"]
		size := len(r.cache)
		r.mu.Unlock()
		wantEq(t, present, false, domain+" cached on error")
		wantEq(t, goodStill, true, "good.example retained after "+domain)
		wantEq(t, size, 1, "cache size after "+domain)
	}

	// Forged TXT record with wrong version is treated as absent, never fetched.
	got, err := r.Lookup("forgedtxt.example")
	mustNoErr(t, err, "Lookup(forgedtxt.example)")
	wantNoPolicy(t, got, "policy for forged TXT record")
	wantEq(t, fetchedBadTXT, false, "HTTPS fetch attempted on forged TXT")
}

func newHostileResolver(now time.Time, fetchedBadTXT *bool) *Resolver {
	return &Resolver{
		Now: func() time.Time { return now },
		LookupTXT: func(name string) ([]string, error) {
			switch name {
			case "_mta-sts.good.example":
				return []string{"v=STSv1; id=1;"}, nil
			case "_mta-sts.servfail.example":
				return nil, &net.DNSError{Err: "server misbehaving"}
			case "_mta-sts.badpolicy.example":
				return []string{"v=STSv1; id=2;"}, nil
			case "_mta-sts.forgedtxt.example":
				return []string{"v=STSv2; id=3;"}, nil
			default:
				return nil, &net.DNSError{IsNotFound: true}
			}
		},
		FetchPolicy: func(domain string) (string, error) {
			if domain == "good.example" {
				return "version: STSv1\nmode: enforce\nmx: mx.good.example\nmax_age: 3600\n", nil
			}
			if domain == "forgedtxt.example" {
				*fetchedBadTXT = true
			}
			return "version: STSv2\nmode: broken\n", nil
		},
	}
}
