package webmail2api

import "testing"

// TestParseKQL checks the field/value split, quoted phrases, and the read/
// attachment booleans. Unknown prefixes fall back to general terms.
func TestParseKQL(t *testing.T) {
	q := parseKQL(`from:alice subject:"quarterly report" has:attachment is:unread leftover`)
	wantOnly(t, "From", q.From, "alice")
	wantOnly(t, "Subject", q.Subject, "quarterly report")
	wantFlag(t, "HasAtt", q.HasAtt, true)
	wantFlag(t, "Read (is:unread)", q.Read, false)
	wantOnly(t, "General", q.General, "leftover")
}

// wantOnly checks a filter carries exactly the one value.
func wantOnly(t *testing.T, label string, got []string, want string) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("%s = %v, want [%s]", label, got, want)
	}
	wantEq(t, label, got[0], want)
}

// wantFlag checks an optional boolean filter is set to the expected value. A nil
// pointer means the filter is absent, which is not the same as false.
func wantFlag(t *testing.T, label string, got *bool, want bool) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is unset, want %v", label, want)
	}
	wantEq(t, label, *got, want)
}

// TestParseKQLUnknownPrefixFallsBack proves an unrecognised "kindle:book" token
// is treated as a general search term, not dropped.
func TestParseKQLUnknownPrefixFallsBack(t *testing.T) {
	q := parseKQL("kindle:book hello")
	if len(q.General) != 2 {
		t.Errorf("General = %v, want 2 terms", q.General)
	}
}

// TestContainsAny proves empty needles match anything and a hit matches.
func TestContainsAny(t *testing.T) {
	if !containsAny("hello world", []string{}) {
		t.Error("empty needles should match")
	}
	if !containsAny("hello world", []string{"world"}) {
		t.Error("should hit on 'world'")
	}
	if containsAny("hello world", []string{"missing"}) {
		t.Error("should miss on 'missing'")
	}
}
