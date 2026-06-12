package imap

import "testing"

func TestParseSeqSetContains(t *testing.T) {
	ss, err := parseSeqSet("1,3,5:7,9:*")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	const max = 12
	in := []uint32{1, 3, 5, 6, 7, 9, 10, 12}
	out := []uint32{2, 4, 8}
	for _, n := range in {
		if !ss.contains(n, max) {
			t.Errorf("contains(%d) = false, want true", n)
		}
	}
	for _, n := range out {
		if ss.contains(n, max) {
			t.Errorf("contains(%d) = true, want false", n)
		}
	}
}

func TestParseSeqSetStar(t *testing.T) {
	// "*" alone resolves to the single largest value in use.
	ss, _ := parseSeqSet("*")
	if !ss.contains(8, 8) || ss.contains(7, 8) {
		t.Errorf("'*' with max 8 should match only 8")
	}
	// A reversed range b:a denotes the same span as a:b (order-independent).
	ss, _ = parseSeqSet("7:3")
	for n := uint32(3); n <= 7; n++ {
		if !ss.contains(n, 100) {
			t.Errorf("7:3 should contain %d", n)
		}
	}
}

func TestParseSeqSetRejects(t *testing.T) {
	for _, bad := range []string{"", "0", "1,0", "a", "1:b", "1,,2"} {
		if _, err := parseSeqSet(bad); err == nil {
			t.Errorf("parseSeqSet(%q) = nil error, want rejection", bad)
		}
	}
}
