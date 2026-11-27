package logging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	s := &MongoSink{ins: ins, spillPath: path}
	s.hasSpill.Store(true)
	s.writeTimeout.Store(int64(mongoWriteTimeout))
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
	after, err := readSpill(path)
	mustNoErr(t, err, "read the spill file")
	if len(after) != len(kept)+1 {
		t.Errorf("with no limit the spill holds %d events, want %d", len(after), len(kept)+1)
	}
}

// TestApplySpillLimitReachesTheSpillingSink applies the operator's cap through
// the logger a daemon holds, the way RunSpillLimit does each minute. The cap
// must land on the spilling sink behind the stderr fan-out, in bytes, and a
// failed read must leave the cap already applied in place.
func TestApplySpillLimitReachesTheSpillingSink(t *testing.T) {
	ms := &MongoSink{}
	l := New(NewMultiSink(NewStderrSink(io.Discard), ms))

	ApplySpillLimit("test", l, func() (int, error) { return 3, nil })
	if got := ms.spillLimit.Load(); got != 3<<20 {
		t.Fatalf("cap = %d bytes, want %d (3 MiB)", got, 3<<20)
	}
	ApplySpillLimit("test", l, func() (int, error) { return 0, errors.New("directory down") })
	if got := ms.spillLimit.Load(); got != 3<<20 {
		t.Errorf("a failed read changed the cap to %d, want it kept at %d", got, 3<<20)
	}
	ApplySpillLimit("test", l, func() (int, error) { return 0, nil })
	if got := ms.spillLimit.Load(); got != 0 {
		t.Errorf("cap = %d after the setting went to 0, want 0 (no cap)", got)
	}
}

// TestSpillRefusesOversizedEventWithoutDroppingExisting verifies that an event
// larger than the configured spill cap is refused and counted while previously
// spilled events remain intact on disk.
func TestSpillRefusesOversizedEventWithoutDroppingExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spill.jsonl")
	s := &MongoSink{spillPath: path}
	const limit = 1024
	s.SetSpillLimit(limit)

	s.spill([]Event{{Subsystem: IMAP, Name: "kept.small"}})
	s.spill([]Event{{Subsystem: IMAP, Name: "too.big", Err: strings.Repeat("z", 2048)}})

	wantEq(t, s.SpillDropped(), uint64(1), "SpillDropped count for oversized event")
	info, err := os.Stat(path)
	mustNoErr(t, err, "stat spill file")
	if info.Size() > limit {
		t.Errorf("spill file size = %d, want at most %d", info.Size(), limit)
	}
	docs, err := readSpill(path)
	mustNoErr(t, err, "read spill file")
	if len(docs) != 1 || docs[0].(mongoDoc).Name != "kept.small" {
		t.Errorf("spill holds %v, want only the 1 pre-existing kept.small event", docs)
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
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("close large spill: %v", err)
		}
	}()
	enc := json.NewEncoder(f)
	doc := toDoc(Event{Subsystem: IMAP, Name: "spill.fill", Err: strings.Repeat("x", 4096)})
	for written := 0; written < size; {
		if err := enc.Encode(doc); err != nil {
			t.Fatal(err)
		}
		written += 4096 + 200
	}
}
