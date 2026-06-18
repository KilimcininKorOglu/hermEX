package logging

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestSpillReplayAcrossRestart proves the degradation path end to end: while the
// inserter fails the events are written to the spill file instead of lost, and a
// later sink over the same file (a restart with MongoDB back) replays the spill on
// its first successful write and removes the file.
func TestSpillReplayAcrossRestart(t *testing.T) {
	spillPath := filepath.Join(t.TempDir(), "spill.jsonl")

	// Run 1: MongoDB is down, so the two events spill to disk.
	down := inserterFunc(func(context.Context, []any) error { return errors.New("mongo down") })
	s1 := newAsyncSink(down, spillPath)
	s1.Write(Event{Subsystem: IMAP, Name: "a"})
	s1.Write(Event{Subsystem: IMAP, Name: "b"})
	ctx1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
	if err := s1.Close(ctx1); err != nil {
		t.Fatalf("run 1 Close: %v", err)
	}
	cancel1()
	if docs, err := readSpill(spillPath); err != nil || len(docs) != 2 {
		t.Fatalf("after run 1 the spill holds %d docs (err %v), want 2", len(docs), err)
	}

	// Run 2: MongoDB is back. The new sink sees the leftover spill, and its first
	// successful write replays it and clears the file.
	var got fakeInserter
	s2 := newAsyncSink(&got, spillPath)
	s2.Write(Event{Subsystem: IMAP, Name: "c"})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	if err := s2.Close(ctx2); err != nil {
		t.Fatalf("run 2 Close: %v", err)
	}
	cancel2()

	if _, total := got.totals(); total != 3 {
		t.Errorf("run 2 inserted %d docs, want 3 (2 replayed + 1 new)", total)
	}
	if spillFileHasData(spillPath) {
		t.Error("the spill file was not drained after a successful replay")
	}
}
