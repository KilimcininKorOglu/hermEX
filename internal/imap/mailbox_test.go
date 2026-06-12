package imap

import "testing"

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
