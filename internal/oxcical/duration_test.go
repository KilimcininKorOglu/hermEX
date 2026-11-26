package oxcical

import (
	"testing"
	"time"
)

// TestParseICalDurationReadsTheGrammar covers RFC 5545 section 3.3.6: each unit
// is one or more digits followed by its designator, and the sign applies to the
// whole value.
func TestParseICalDurationReadsTheGrammar(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"PT15M", 15 * time.Minute},
		{"-PT1H30M", -90 * time.Minute},
		{"P1D", 24 * time.Hour},
		{"P2W", 14 * 24 * time.Hour},
		{"P1DT2H3M4S", 26*time.Hour + 3*time.Minute + 4*time.Second},
	}
	for _, c := range cases {
		got, ok := parseICalDuration(c.in)
		if !ok || got != c.want {
			t.Errorf("parseICalDuration(%q) = %v, %v; want %v, true", c.in, got, ok, c.want)
		}
	}
}

// TestParseICalDurationRefusesWhatItCannotHold covers a DURATION a sender
// controls. A count too large for a Go duration (about 292 years) wrapped into a
// negative or unrelated length that was then accepted as the event's span; a
// designator without digits, or digits without a designator, is not a duration
// under the RFC 5545 section 3.3.6 grammar.
func TestParseICalDurationRefusesWhatItCannotHold(t *testing.T) {
	for _, in := range []string{
		"P200000D",
		"P99999999999999999999D",
		"PT9223372036854775807S",
		"P100000W",
		"P150000DT150000D",
		"PD",
		"PT5",
	} {
		if got, ok := parseICalDuration(in); ok {
			t.Errorf("parseICalDuration(%q) = %v, true; want refused", in, got)
		}
	}
}
