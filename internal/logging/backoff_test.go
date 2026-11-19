package logging

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestASpillDuringReplayIsKept spills a new event while a replay is still
// inserting the old spill, as the batching goroutine does when the store is busy.
// The replay removes what it replayed; the new event must survive it.
func TestASpillDuringReplayIsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spill.jsonl")
	s := &MongoSink{spillPath: path}
	s.writeTimeout.Store(int64(time.Second))
	s.spill([]Event{{Subsystem: IMAP, Name: "old"}})

	var replayed int
	s.ins = inserterFunc(func(_ context.Context, docs []any) error {
		if replayed == 0 {
			s.spill([]Event{{Subsystem: IMAP, Name: "during.replay"}})
		}
		replayed += len(docs)
		return nil
	})
	s.replaySpill()

	if replayed != 1 {
		t.Fatalf("the replay inserted %d events, want the 1 spilled before it", replayed)
	}
	docs, err := readSpill(path)
	if err != nil || len(docs) != 1 || docs[0].(mongoDoc).Name != "during.replay" {
		t.Errorf("after the replay the spill holds %v (err %v), want the event spilled during it", docs, err)
	}
	if !s.hasSpill.Load() {
		t.Error("the sink forgot it still has a spill to replay")
	}
}

// TestADownStoreIsNotRetriedForEveryBatch takes the log store down after the sink
// connected, so every insert hangs until its deadline. Retrying the store for each
// batch held the single writer for the whole deadline per batch, the buffer filled,
// and the events were dropped instead of spilled. After a failure the sink must
// spill straight to disk for a while, so a burst of events reaches the spill file.
func TestADownStoreIsNotRetriedForEveryBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spill.jsonl")
	var calls atomic.Int32
	hang := inserterFunc(func(ctx context.Context, _ []any) error {
		calls.Add(1)
		<-ctx.Done()
		return errors.New("store down")
	})
	s := newAsyncSink(readyConn(hang), path)
	// Long enough that a writer waiting on the store overflows the buffer.
	s.writeTimeout.Store(int64(time.Second))

	const events = 20 * mongoBatchSize
	for i := range events {
		s.Write(Event{Subsystem: IMAP, Name: "burst"})
		if i%mongoBatchSize == 0 {
			time.Sleep(5 * time.Millisecond) // a busy daemon, not one tight loop
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if got := calls.Load(); got > 2 {
		t.Errorf("the down store was tried %d times for %d batches, want it skipped while backing off", got, events/mongoBatchSize)
	}
	docs, err := readSpill(path)
	if err != nil {
		t.Fatal(err)
	}
	// The store is written from its own goroutine, so even the attempt that finds
	// it down holds nothing up: every event reaches the spill file.
	if len(docs) != events || s.Dropped() != 0 {
		t.Errorf("spilled %d of %d events (dropped %d), want every event spilled", len(docs), events, s.Dropped())
	}
}
