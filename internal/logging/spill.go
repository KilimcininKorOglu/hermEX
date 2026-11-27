package logging

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// spill appends the events' stored documents to the spill file as
// newline-delimited JSON, to be replayed once MongoDB is reachable again. With no
// spill path configured (or on an I/O error) the events are unrecorded, there is
// nowhere left to put them. Both the batching and the store goroutine spill, so
// spillMu keeps their appends whole and apart from the replay's hand-off.
func (s *MongoSink) spill(events []Event) {
	if s.spillPath == "" || len(events) == 0 {
		return
	}
	s.spillMu.Lock()
	defer s.spillMu.Unlock()
	f, err := os.OpenFile(s.spillPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		s.spillLost(len(events), "open", err)
		return
	}
	defer s.closeSpillFile(f, len(events))
	size := int64(0)
	if info, err := f.Stat(); err == nil {
		size = info.Size()
	}
	limit := s.spillLimit.Load()
	for i, e := range events {
		line, err := json.Marshal(toDoc(e))
		if err != nil {
			continue // an unencodable event cannot be stored anywhere
		}
		line = append(line, '\n')
		if limit > 0 && size+int64(len(line)) > limit {
			s.spillDropped.Add(uint64(len(events) - i))
			s.spillLost(len(events)-i, "limit", nil)
			return
		}
		if _, err := f.Write(line); err != nil {
			s.spillLost(len(events)-i, "write", err)
			return
		}
		size += int64(len(line))
		s.hasSpill.Store(true)
	}
}

// closeSpillFile closes f and reports any close error to stderr.
func (s *MongoSink) closeSpillFile(f *os.File, n int) {
	if cerr := f.Close(); cerr != nil {
		s.spillLost(n, "close", cerr)
	}
}

// replayPath is where a spill is moved while it is replayed, so events spilled
// during the replay land in a fresh spill file instead of one about to be removed.
func replayPath(spillPath string) string {
	if spillPath == "" {
		return ""
	}
	return spillPath + ".replay"
}

// spillLost tells the operator, on stderr (the only place left once both the log
// store and the spill file are out), how many events could not be kept and why.
func (s *MongoSink) spillLost(n int, reason string, err error) {
	msg := ""
	if err != nil {
		msg = ": " + err.Error()
	}
	if _, werr := fmt.Fprintf(os.Stderr, "logging: %d events not spilled to %s (%s)%s\n", n, s.spillPath, reason, msg); werr != nil {
		s.stderrErrors.Add(1)
	}
}

// replaySpill re-inserts everything in the spill file in batches and removes the
// file once it all lands. A failure mid-replay leaves the file in place to retry
// on the next successful write; because the documents carry no _id a retry may
// re-insert an already-written batch, at-least-once delivery, which keeps the
// audit trail complete at the cost of an occasional duplicate (acceptable for
// logs). Only the store goroutine calls this. The spill is first moved aside to
// replayPath under spillMu, so a batch spilled while the replay runs goes to a new
// spill file and is replayed next time rather than removed unread; an earlier
// replay that failed is finished before a new spill is moved aside. It reports
// false when the store refused the replay.
func (s *MongoSink) replaySpill() bool {
	if !s.hasSpill.Load() || s.spillPath == "" {
		return true
	}
	replaying := replayPath(s.spillPath)
	s.spillMu.Lock()
	if _, err := os.Stat(replaying); errors.Is(err, fs.ErrNotExist) {
		if err := os.Rename(s.spillPath, replaying); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.spillMu.Unlock()
			return true // a disk problem, not the store: keep the file, retry later
		}
	}
	s.hasSpill.Store(false) // a spill from here on sets it again
	s.spillMu.Unlock()

	insert := func(batch []any) error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.writeTimeout.Load()))
		defer cancel()
		return s.ins.InsertMany(ctx, batch)
	}
	if err := eachSpillBatch(replaying, insert); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		s.hasSpill.Store(true) // unreachable again: keep it, retry later
		return false
	}
	// The batches are already in Mongo. A failed removal replays them on the next
	// drain, which duplicates events rather than losing them: the safe direction,
	// and the only one available to the logger itself.
	if err := os.Remove(replaying); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.spillLost(0, "remove", err)
	}
	return true
}

// eachSpillBatch decodes the newline-delimited JSON spill file into insertable
// documents (typed mongoDoc values, so the timestamp re-encodes as a BSON date on
// re-insert) and hands them to fn at most mongoBatchSize at a time, reading the
// file as it goes: a spill from a long outage can be larger than the daemon's
// memory, so it is never loaded whole. A torn trailing record from a crash
// mid-spill is tolerated: decoding stops at the first malformed value and the
// records that decoded cleanly are replayed. The first error from fn stops the
// pass and is returned.
func eachSpillBatch(path string, fn func([]any) error) (err error) {
	// #nosec G304 -- the spill file lives in the daemon's own spill directory, derived from the config
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
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
