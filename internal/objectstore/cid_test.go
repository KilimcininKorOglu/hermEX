package objectstore

import (
	"bytes"
	"os"
	"testing"
)

func TestContentRoundTripAndDedup(t *testing.T) {
	s := openTestStore(t)

	// Distinct-byte vectors: empty, single byte, every byte value, and a larger
	// repetitive blob (exercises compression).
	big := make([]byte, 64*1024)
	for i := range big {
		big[i] = byte(i * 7)
	}
	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
	}
	vectors := [][]byte{
		{},
		{0x00},
		{0xFF},
		allBytes,
		big,
	}

	seen := map[string]bool{}
	for i, v := range vectors {
		cid, err := s.putContent(v)
		if err != nil {
			t.Fatalf("vector %d put: %v", i, err)
		}
		if seen[cid] {
			t.Errorf("vector %d produced a duplicate cid %q", i, cid)
		}
		seen[cid] = true

		got, err := s.getContent(cid)
		if err != nil {
			t.Fatalf("vector %d get: %v", i, err)
		}
		// Empty content round-trips to empty (len 0), not nil-sensitive.
		if !bytes.Equal(got, v) {
			t.Errorf("vector %d round-trip mismatch: got %d bytes, want %d", i, len(got), len(v))
		}
	}

	// Dedup: storing identical content twice yields the same cid and one file.
	cid1, _ := s.putContent(allBytes)
	cid2, _ := s.putContent(allBytes)
	if cid1 != cid2 {
		t.Errorf("dedup: cid mismatch %q vs %q", cid1, cid2)
	}
	if fi, err := os.Stat(s.cidPath(cid1)); err != nil || fi.Size() == 0 {
		t.Errorf("content file missing or empty for %q: %v", cid1, err)
	}

	// The cid format is the sharded "S-XX/<62 hex>" form.
	if len(cid1) != len("S-")+2+1+62 || cid1[:2] != "S-" || cid1[4] != '/' {
		t.Errorf("unexpected cid format: %q", cid1)
	}
}
