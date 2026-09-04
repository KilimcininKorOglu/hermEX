package ics

import (
	"encoding/binary"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestMultivalueCountCheckedAgainstRemaining is the allocation guard. The element
// count is a 32-bit field the stream length does not bound, and the absolute cap
// still admits a count the stream cannot possibly satisfy, so five bytes of input
// could reserve megabytes. Every element costs at least two wire bytes, so the
// count has to be checked against what is left before it becomes a make() length.
func TestMultivalueCountCheckedAgainstRemaining(t *testing.T) {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, 1<<20) // under the absolute cap, far over the buffer

	_, _, ok, err := decodeMV(buf, mapi.PtMvI8)
	if ok {
		t.Fatal("a count the stream cannot satisfy was accepted")
	}
	if err == nil || !strings.Contains(err.Error(), "bytes left") {
		t.Fatalf("refusal = %v, want it to name the remaining bytes", err)
	}
}

// TestOrdinaryMultivalueStillDecodes is the control: a real multivalue must still
// round-trip, so the guard cannot be a blanket refusal.
func TestOrdinaryMultivalueStillDecodes(t *testing.T) {
	want := []int64{7, -3, 1 << 40}
	buf, err := appendMV(nil, mapi.PtMvI8, want)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	got, _, ok, err := decodeMV(buf, mapi.PtMvI8)
	if err != nil || !ok {
		t.Fatalf("ordinary multivalue refused: ok=%v err=%v", ok, err)
	}
	xs, isSlice := got.([]int64)
	if !isSlice || len(xs) != len(want) {
		t.Fatalf("decoded %T %v, want %v", got, got, want)
	}
	for i := range want {
		if xs[i] != want[i] {
			t.Errorf("element %d = %d, want %d", i, xs[i], want[i])
		}
	}
}
