package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
)

// spill appends the events' stored documents to the spill file as
// newline-delimited JSON, to be replayed once MongoDB is reachable again. With no
// spill path configured (or on an I/O error) the events are unrecorded — there is
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
// re-insert an already-written batch — at-least-once delivery, which keeps the
// audit trail complete at the cost of an occasional duplicate (acceptable for
// logs). Only the run() goroutine calls this.
func (s *MongoSink) replaySpill() {
	if !s.hasSpill || s.spillPath == "" {
		return
	}
	docs, err := readSpill(s.spillPath)
	if err != nil {
		return // cannot read it now; keep it and retry on the next success
	}
	if len(docs) == 0 {
		os.Remove(s.spillPath)
		s.hasSpill = false
		return
	}
	for i := 0; i < len(docs); i += mongoBatchSize {
		end := min(i+mongoBatchSize, len(docs))
		ctx, cancel := context.WithTimeout(context.Background(), mongoWriteTimeout)
		err := s.ins.InsertMany(ctx, docs[i:end])
		cancel()
		if err != nil {
			return // unreachable again — keep the file, retry later
		}
	}
	os.Remove(s.spillPath)
	s.hasSpill = false
}

// readSpill decodes the newline-delimited JSON spill file into insertable
// documents (typed mongoDoc values, so the timestamp re-encodes as a BSON date on
// re-insert). A torn trailing record from a crash mid-spill is tolerated: decoding
// stops at the first malformed value and replays the records that decoded cleanly.
func readSpill(path string) ([]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var docs []any
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var d mongoDoc
		if err := dec.Decode(&d); err != nil {
			if err == io.EOF {
				break
			}
			break // a truncated final record — keep what decoded cleanly
		}
		docs = append(docs, d)
	}
	return docs, nil
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
