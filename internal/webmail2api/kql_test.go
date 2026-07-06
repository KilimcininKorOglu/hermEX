package webmail2api

import "testing"

// TestParseKQL checks the field/value split, quoted phrases, and the read/
// attachment booleans. Unknown prefixes fall back to general terms.
func TestParseKQL(t *testing.T) {
	q := parseKQL(`from:alice subject:"quarterly report" has:attachment is:unread leftover`)
	if len(q.From) != 1 || q.From[0] != "alice" {
		t.Errorf("From = %v, want [alice]", q.From)
	}
	if len(q.Subject) != 1 || q.Subject[0] != "quarterly report" {
		t.Errorf("Subject = %v, want [quarterly report]", q.Subject)
	}
	if q.HasAtt == nil || !*q.HasAtt {
		t.Errorf("HasAtt = %v, want true", q.HasAtt)
	}
	if q.Read == nil || *q.Read {
		t.Errorf("Read = %v, want false (unread)", q.Read)
	}
	if len(q.General) != 1 || q.General[0] != "leftover" {
		t.Errorf("General = %v, want [leftover]", q.General)
	}
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
