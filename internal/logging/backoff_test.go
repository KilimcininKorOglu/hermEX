package logging

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

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
	s.writeTimeout.Store(int64(100 * time.Millisecond))

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
	// The one attempt that finds the store down still holds the writer for its
	// deadline, so a burst during it can overflow the buffer; every such event
	// must be counted, never lost unseen.
	if lost := events - len(docs); lost != int(s.Dropped()) {
		t.Errorf("spilled %d of %d events but counted %d dropped: %d were lost unseen",
			len(docs), events, s.Dropped(), lost-int(s.Dropped()))
	}
}
