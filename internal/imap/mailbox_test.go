package imap

import (
	"strings"
	"testing"
	"time"
)

// TestIMAPMatchHostilePatternIsBounded runs a LIST pattern of many wildcards
// that cannot match against a long name. The pattern is client input, so its
// cost must grow with pattern length times name length, not exponentially: a
// backtracking matcher spends years on this pair and holds a server thread. The
// bound is generous (2s) and the expected answer comes from RFC 3501 6.3.8: the
// name has no "b", so nothing matches.
func TestIMAPMatchHostilePatternIsBounded(t *testing.T) {
	pattern := strings.Repeat("*a", 40) + "b"
	name := strings.Repeat("a", 200)
	done := make(chan bool, 1)
	go func() { done <- imapMatch(pattern, name) || imapMatch(strings.ReplaceAll(pattern, "*", "%"), name) }()
	select {
	case got := <-done:
		if got {
			t.Error("a pattern ending in b matched a name with no b")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("matching a hostile LIST pattern did not finish in 2s")
	}
}

func TestIMAPMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*", "INBOX", true},
		{"*", "Archive/2026", true},
		{"%", "INBOX", true},
		{"%", "Archive/2026", false}, // '%' does not cross the separator
		{"Archive/%", "Archive/2026", true},
		{"Archive/%", "Archive/2026/Q1", false}, // '%' stops at the next level
		{"Archive/*", "Archive/2026/Q1", true},  // '*' crosses separators
		{"INBOX", "INBOX", true},
		{"INBOX", "Inbox", false}, // matcher itself is case-sensitive
		{"Sent", "Sent", true},
		{"Sent", "Sentinel", false},
	}
	for _, tc := range cases {
		if got := imapMatch(tc.pattern, tc.name); got != tc.want {
			t.Errorf("imapMatch(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}
