package mta

import "testing"

// TestNotifyFailureWanted pins the RFC 3461 suppression decision: a bounce is
// emitted only when the sender did not opt out of failure notices. The cases
// that must return false (NEVER, and any list omitting FAILURE) are the
// backscatter guard; getting them wrong sends an unwanted bounce.
func TestNotifyFailureWanted(t *testing.T) {
	cases := []struct {
		notify string
		want   bool
	}{
		{"", true},                // absent: pre-DSN default is to bounce on failure
		{"NEVER", false},          // explicit opt-out
		{"never", false},          // case-insensitive
		{"FAILURE", true},         // explicitly wants failure
		{"SUCCESS,FAILURE", true}, // failure among others
		{"SUCCESS,DELAY", false},  // wants success/delay but not failure: suppress
		{"DELAY", false},          // delay only
		{"success,failure", true}, // lower case in a list
		{" FAILURE ", true},       // surrounding space tolerated
		{"FAILURE,SUCCESS", true}, // order-independent
	}
	for _, c := range cases {
		if got := NotifyFailureWanted(c.notify); got != c.want {
			t.Errorf("NotifyFailureWanted(%q) = %v, want %v", c.notify, got, c.want)
		}
	}
}
