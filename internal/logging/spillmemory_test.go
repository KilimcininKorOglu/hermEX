package logging

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReplayHoldsOneBatchAtATime replays a spill file left by a long log-store
// outage. The file can be far larger than the daemon's memory budget, and the
// daemon replaying it may be the MTA, so the replay must read and insert it batch
// by batch rather than load all of it before the first insert.
func TestReplayHoldsOneBatchAtATime(t *testing.T) {
	const spillBytes = 64 << 20
	const heapBudget = 16 << 20
	path := filepath.Join(t.TempDir(), "spill.jsonl")
	writeLargeSpill(t, path, spillBytes)

	var peak uint64
	var docs int
	ins := inserterFunc(func(_ context.Context, batch []any) error {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		peak = max(peak, ms.HeapAlloc)
		docs += len(batch)
		return nil
	})
	s := &MongoSink{ins: ins, spillPath: path, hasSpill: true}
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	s.replaySpill()

	if docs == 0 {
		t.Fatal("the replay inserted nothing")
	}
	if spillFileHasData(path) {
		t.Error("the spill file was not removed after a complete replay")
	}
	if grown := int64(peak) - int64(base.HeapAlloc); grown > heapBudget {
		t.Errorf("replaying a %d MiB spill held %d MiB live at an insert, want at most %d MiB: the whole file was loaded",
			spillBytes>>20, grown>>20, heapBudget>>20)
	}
}

// TestSpillStopsAtTheLimit fills the spill past the operator's cap during an
// outage. The file must stay within the cap, so the disk the mailboxes share
// never fills, and every event turned away must be counted rather than vanish.
func TestSpillStopsAtTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spill.jsonl")
	s := &MongoSink{spillPath: path}
	const limit = 64 << 10
	s.SetSpillLimit(limit)

	events := make([]Event, 100)
	for i := range events {
		events[i] = Event{Subsystem: IMAP, Name: "spill.fill", Err: strings.Repeat("x", 4096)}
	}
	s.spill(events)
	s.spill(events)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > limit {
		t.Errorf("spill file is %d bytes, want at most the %d byte limit", info.Size(), limit)
	}
	kept, err := readSpill(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) == 0 {
		t.Fatal("nothing was spilled below the limit")
	}
	if got, want := s.SpillDropped(), uint64(200-len(kept)); got != want {
		t.Errorf("SpillDropped = %d, want %d (every event not kept)", got, want)
	}

	// Lifting the cap lets the next outage batch through again.
	s.SetSpillLimit(0)
	s.spill(events[:1])
	if after, _ := readSpill(path); len(after) != len(kept)+1 {
		t.Errorf("with no limit the spill holds %d events, want %d", len(after), len(kept)+1)
	}
}

// readSpill collects every record the replay would insert from the spill file.
func readSpill(path string) ([]any, error) {
	var docs []any
	err := eachSpillBatch(path, func(batch []any) error {
		docs = append(docs, batch...)
		return nil
	})
	return docs, err
}

// writeLargeSpill writes spill records until the file reaches size bytes.
func writeLargeSpill(t *testing.T, path string, size int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	doc := toDoc(Event{Subsystem: IMAP, Name: "spill.fill", Err: strings.Repeat("x", 4096)})
	for written := 0; written < size; {
		if err := enc.Encode(doc); err != nil {
			t.Fatal(err)
		}
		written += 4096 + 200
	}
}
