package recurrence

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestFromRRuleRejectsUnrepresentableInterval is the divide-by-zero guard. The
// daily pattern's period is the interval in minutes, and the field is 32 bits, so
// an interval a multiple of 2^27 multiplied by 1440 lands on exactly zero modulo
// 2^32. The next line takes StartDate modulo that period, which is an integer
// division by zero: a runtime panic no caller can recover from, reached from any
// path that stores a client's RRULE (CalDAV PUT, EWS, ActiveSync, webmail).
func TestFromRRuleRejectsUnrepresentableInterval(t *testing.T) {
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	for _, rule := range []string{
		"FREQ=DAILY;INTERVAL=134217728",
		"FREQ=DAILY;INTERVAL=268435456",
		"FREQ=DAILY;INTERVAL=9223372036854775807",
		"FREQ=WEEKLY;INTERVAL=4294967297",
		"FREQ=YEARLY;INTERVAL=4294967296",
	} {
		_, err := FromRRule(rule, start)
		if err == nil {
			t.Errorf("%s was accepted", rule)
			continue
		}
		if !errors.Is(err, ErrInvalidRRule) {
			t.Errorf("%s gave %v, want an ErrInvalidRRule", rule, err)
		}
		if !strings.Contains(err.Error(), "INTERVAL") {
			t.Errorf("%s gave %q, which does not name the field that failed", rule, err)
		}
	}
}

// TestFromRRuleKeepsOrdinaryIntervals keeps the intervals a real client sends.
func TestFromRRuleKeepsOrdinaryIntervals(t *testing.T) {
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	for _, rule := range []string{
		"FREQ=DAILY;INTERVAL=1",
		"FREQ=DAILY;INTERVAL=3;COUNT=10",
		"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE",
		"FREQ=MONTHLY;INTERVAL=6;BYMONTHDAY=15",
		"FREQ=YEARLY;INTERVAL=1;BYMONTH=6;BYMONTHDAY=1",
	} {
		if _, err := FromRRule(rule, start); err != nil {
			t.Errorf("%s was refused: %v", rule, err)
		}
	}
}

// TestFromRRuleNamesTheFieldItRejected keeps every rejection diagnosable. A bare
// "invalid RRULE" leaves an operator unable to tell a client that omitted FREQ from
// one that sent a value the pattern cannot carry, and those call for different
// answers.
func TestFromRRuleNamesTheFieldItRejected(t *testing.T) {
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		rule  string
		names string
	}{
		{"INTERVAL=2;BYMONTHDAY=1", "FREQ"},
		{"FREQ=DAILY;INTERVAL=134217728", "INTERVAL"},
		{"FREQ=DAILY;COUNT=4294967296", "COUNT"},
	}
	for _, c := range cases {
		_, err := FromRRule(c.rule, start)
		if err == nil {
			t.Errorf("%s was accepted", c.rule)
			continue
		}
		if !errors.Is(err, ErrInvalidRRule) {
			t.Errorf("%s gave %v, want an ErrInvalidRRule", c.rule, err)
		}
		if !strings.Contains(err.Error(), c.names) {
			t.Errorf("%s gave %q, want it to name %s", c.rule, err, c.names)
		}
	}
}
