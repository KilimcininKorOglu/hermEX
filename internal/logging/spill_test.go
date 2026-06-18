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
	s1 := newAsyncSink(readyConn(down), spillPath)
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
	s2 := newAsyncSink(readyConn(&got), spillPath)
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

// TestConnectFailureSpillsThenRecovers proves the boot-resilience contract: when
// the store is unreachable at startup the connection itself fails (not just a
// write), and the event must spill rather than be lost; once the store comes up a
// later sink connects and replays the spill even with no new events to write.
func TestConnectFailureSpillsThenRecovers(t *testing.T) {
	spillPath := filepath.Join(t.TempDir(), "spill.jsonl")

	// Run 1: the store is unreachable, so connect fails outright. The daemon kept
	// running; its startup event must land on disk instead of vanishing.
	downConn := func(context.Context) (inserter, error) { return nil, errors.New("store unreachable") }
	s1 := newAsyncSink(downConn, spillPath)
	s1.Write(Event{Subsystem: System, Name: "daemon.startup"})
	ctx1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
	if err := s1.Close(ctx1); err != nil {
		t.Fatalf("run 1 Close: %v", err)
	}
	cancel1()
	if !spillFileHasData(spillPath) {
		t.Fatal("a connect failure lost the event instead of spilling it")
	}

	// Run 2: the store is back. With nothing new buffered, the sink must still
	// reconnect and drain the spill — the self-heal the user chose.
	var got fakeInserter
	s2 := newAsyncSink(readyConn(&got), spillPath)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	if err := s2.Close(ctx2); err != nil {
		t.Fatalf("run 2 Close: %v", err)
	}
	cancel2()
	if _, total := got.totals(); total != 1 {
		t.Errorf("recovered sink replayed %d docs, want 1", total)
	}
	if spillFileHasData(spillPath) {
		t.Error("spill not drained after the store recovered")
	}
}
