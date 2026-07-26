package mta

import "testing"

// TestSameDomain pins the cross-tenant guard used by the out-of-office decision:
// only two addresses in the same domain count as internal, and a missing domain
// on either side is never a match.
func TestSameDomain(t *testing.T) {
	match := [][2]string{
		{"a@example.com", "b@example.com"},
		{"a@Example.com", "b@example.COM"}, // case-insensitive
	}
	for _, p := range match {
		if !sameDomain(p[0], p[1]) {
			t.Errorf("sameDomain(%q,%q) = false, want true", p[0], p[1])
		}
	}
	noMatch := [][2]string{
		{"a@example.com", "b@other.com"},
		{"a@example.com", "b"},  // no domain on one side
		{"a", "b@example.com"},  // no domain on one side
		{"a@", "b@example.com"}, // empty domain
	}
	for _, p := range noMatch {
		if sameDomain(p[0], p[1]) {
			t.Errorf("sameDomain(%q,%q) = true, want false", p[0], p[1])
		}
	}
}
