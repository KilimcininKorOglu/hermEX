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
