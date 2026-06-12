package mapi

import (
	"testing"
	"time"
)

func TestNTTimeEpochVector(t *testing.T) {
	// The Unix epoch is 11644473600 seconds after the FILETIME epoch, i.e.
	// 11644473600 * 10^7 hundred-nanosecond ticks.
	if got := UnixToNTTime(time.Unix(0, 0)); got != 11644473600*10000000 {
		t.Fatalf("epoch nttime = %d, want %d", got, uint64(11644473600*10000000))
	}
}

func TestNTTimeRoundTrip(t *testing.T) {
	// Build a time at 100ns resolution so the conversion is lossless.
	want := time.Date(2026, 6, 12, 13, 37, 42, 2500*100, time.UTC)
	got := NTTimeToUnix(UnixToNTTime(want))
	if !got.Equal(want) {
		t.Fatalf("round-trip = %v, want %v", got, want)
	}
}
