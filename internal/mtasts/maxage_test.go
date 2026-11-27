package mtasts

import (
	"testing"
	"time"
)

// TestACachedPolicyLivesAtMostAYear publishes a policy whose max_age is ten
// years. RFC 8461 3.2 caps max_age at 31557600 seconds, so a domain that later
// relaxes or withdraws its policy is seen within a year at most, and an
// oversized value must not overflow into an expiry that is already past.
func TestACachedPolicyLivesAtMostAYear(t *testing.T) {
	for _, maxAge := range []string{"315576000", "9223372036854775807"} {
		fetches := 0
		now := time.Unix(1_700_000_000, 0)
		r := &Resolver{
			LookupTXT: func(string) ([]string, error) { return []string{"v=STSv1; id=1;"}, nil },
			FetchPolicy: func(string) (string, error) {
				fetches++
				return "version: STSv1\nmode: enforce\nmx: mx.example.com\nmax_age: " + maxAge + "\n", nil
			},
			Now: func() time.Time { return now },
		}
		for _, step := range []struct {
			at   time.Duration
			want int
		}{{0, 1}, {time.Hour, 1}, {31557600 * time.Second, 1}, {31557601 * time.Second, 2}} {
			now = time.Unix(1_700_000_000, 0).Add(step.at)
			_, err := r.Lookup("example.com")
			mustNoErr(t, err, "lookup")
			if fetches != step.want {
				t.Errorf("max_age %s: fetches after %v = %d, want %d", maxAge, step.at, fetches, step.want)
			}
		}
	}
}

// TestHostileMaxAgeRefusedWithoutCaching checks that invalid, negative, zero,
// and overflowing max_age values are rejected by Lookup without modifying the
// resolver cache (RFC 8461 3.2 requires a positive integer up to 31557600).
func TestHostileMaxAgeRefusedWithoutCaching(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, bad := range []string{"-1", "0", "abc", "", "99999999999999999999999999"} {
		r := &Resolver{
			LookupTXT: func(string) ([]string, error) { return []string{"v=STSv1; id=1;"}, nil },
			FetchPolicy: func(domain string) (string, error) {
				if domain == "good.example" {
					return "version: STSv1\nmode: enforce\nmx: mx.good.example\nmax_age: 3600\n", nil
				}
				return "version: STSv1\nmode: enforce\nmx: mx.bad.example\nmax_age: " + bad + "\n", nil
			},
			Now: func() time.Time { return now },
		}
		if _, err := r.Lookup("good.example"); err != nil {
			t.Fatalf("seed good.example: %v", err)
		}
		got, err := r.Lookup("bad.example")
		if err == nil {
			t.Errorf("max_age %q: expected error, got nil", bad)
		}
		wantNoPolicy(t, got, "policy for invalid max_age "+bad)
		r.mu.Lock()
		_, cachedBad := r.cache["bad.example"]
		_, cachedGood := r.cache["good.example"]
		cacheLen := len(r.cache)
		r.mu.Unlock()
		wantEq(t, cachedBad, false, "bad.example cached for max_age "+bad)
		wantEq(t, cachedGood, true, "good.example retained for max_age "+bad)
		wantEq(t, cacheLen, 1, "cache size after hostile max_age "+bad)
	}
}
