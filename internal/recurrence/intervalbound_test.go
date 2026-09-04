package recurrence

import (
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
		if _, err := FromRRule(rule, start); err == nil {
			t.Errorf("%s was accepted", rule)
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
