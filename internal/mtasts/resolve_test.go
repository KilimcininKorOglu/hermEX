package mtasts

import (
	"errors"
	"net"
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
// cached as "no policy" — otherwise a blip would silently disable enforcement.
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
