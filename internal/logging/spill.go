package logging

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
)

// spill appends the events' stored documents to the spill file as
// newline-delimited JSON, to be replayed once MongoDB is reachable again. With no
// spill path configured (or on an I/O error) the events are unrecorded, there is
// nowhere left to put them. Only the run() goroutine calls this, so the file and
// hasSpill need no synchronization.
func (s *MongoSink) spill(events []Event) {
	if s.spillPath == "" {
		return
	}
	f, err := os.OpenFile(s.spillPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(toDoc(e)); err != nil {
			return
		}
	}
	s.hasSpill = true
}

// replaySpill re-inserts everything in the spill file in batches and removes the
// file once it all lands. A failure mid-replay leaves the file in place to retry
// on the next successful write; because the documents carry no _id a retry may
// re-insert an already-written batch, at-least-once delivery, which keeps the
// audit trail complete at the cost of an occasional duplicate (acceptable for
// logs). Only the run() goroutine calls this.
func (s *MongoSink) replaySpill() {
	if !s.hasSpill || s.spillPath == "" {
		return
	}
	insert := func(batch []any) error {
		ctx, cancel := context.WithTimeout(context.Background(), mongoWriteTimeout)
		defer cancel()
		return s.ins.InsertMany(ctx, batch)
	}
	if err := eachSpillBatch(s.spillPath, insert); err != nil {
		return // unreadable now or unreachable again: keep the file, retry later
	}
	// The batches are already in Mongo. A failed removal replays them on the next
	// drain, which duplicates events rather than losing them: the safe direction,
	// and the only one available to the logger itself.
	_ = os.Remove(s.spillPath)
	s.hasSpill = false
}

// eachSpillBatch decodes the newline-delimited JSON spill file into insertable
// documents (typed mongoDoc values, so the timestamp re-encodes as a BSON date on
// re-insert) and hands them to fn at most mongoBatchSize at a time, reading the
// file as it goes: a spill from a long outage can be larger than the daemon's
// memory, so it is never loaded whole. A torn trailing record from a crash
// mid-spill is tolerated: decoding stops at the first malformed value and the
// records that decoded cleanly are replayed. The first error from fn stops the
// pass and is returned.
func eachSpillBatch(path string, fn func([]any) error) error {
	// #nosec G304 -- the spill file lives in the daemon's own spill directory, derived from the config
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	batch := make([]any, 0, mongoBatchSize)
	for {
		var d mongoDoc
		if dec.Decode(&d) != nil {
			break // end of file, or a truncated final record: keep what decoded cleanly
		}
		if batch = append(batch, d); len(batch) < mongoBatchSize {
			continue
		}
		if err := fn(batch); err != nil {
			return err
		}
		batch = make([]any, 0, mongoBatchSize)
	}
	if len(batch) == 0 {
		return nil
	}
	return fn(batch)
}

// spillFileHasData reports whether path exists and is non-empty, so a starting
// sink knows to replay a spill a previous run left behind.
func spillFileHasData(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}
