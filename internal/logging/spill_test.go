package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
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

func mustSpillFile(t *testing.T, dir, daemon string) string {
	t.Helper()
	path, err := spillFile(dir, daemon)
	if err != nil {
		t.Fatalf("spillFile(%q): %v", daemon, err)
	}
	return path
}

// TestDaemonsSharingASpillDirectoryKeepEachOthersEvents runs two daemons on one
// host with one log_spill_dir while the store is down. When the store returns and
// one daemon replays, the other daemon's spilled events must still be on disk:
// replay removes the file it read, so a file the two shared would lose them.
func TestDaemonsSharingASpillDirectoryKeepEachOthersEvents(t *testing.T) {
	dir := t.TempDir()
	down := inserterFunc(func(context.Context, []any) error { return errors.New("mongo down") })
	closeSink := func(s *MongoSink) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}

	imap := newAsyncSink(readyConn(down), mustSpillFile(t, dir, "hermex-imap"))
	imap.Write(Event{Subsystem: IMAP, Name: "imap.event"})
	closeSink(imap)
	pop3 := newAsyncSink(readyConn(down), mustSpillFile(t, dir, "hermex-pop3"))
	pop3.Write(Event{Subsystem: POP3, Name: "pop3.event"})
	closeSink(pop3)

	var got fakeInserter
	closeSink(newAsyncSink(readyConn(&got), mustSpillFile(t, dir, "hermex-pop3")))
	if _, total := got.totals(); total != 1 {
		t.Errorf("the pop3 replay inserted %d events, want only its own 1", total)
	}
	if docs, err := readSpill(mustSpillFile(t, dir, "hermex-imap")); err != nil || len(docs) != 1 {
		t.Errorf("imap's spill holds %d events (err %v) after pop3 replayed, want its 1 kept", len(docs), err)
	}
}

// TestALegacySpillFileIsReplayedUnderTheNewName starts a daemon in a directory
// left by a release that wrote every daemon's spill to one file name. That file
// is this daemon's (each had its own directory then), and its events must still
// be replayed; a file already under the new name is never overwritten by it.
func TestALegacySpillFileIsReplayedUnderTheNewName(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, legacySpillName)
	spillTo := func(path, name string) {
		t.Helper()
		down := inserterFunc(func(context.Context, []any) error { return errors.New("mongo down") })
		s := newAsyncSink(readyConn(down), path)
		s.Write(Event{Subsystem: IMAP, Name: name})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	spillTo(legacy, "before.upgrade")
	path := mustSpillFile(t, dir, "hermex-imap")
	if docs, err := readSpill(path); err != nil || len(docs) != 1 {
		t.Fatalf("the daemon's spill holds %d events (err %v), want the legacy event", len(docs), err)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("legacy file still present (err %v), its events would replay twice", err)
	}

	spillTo(legacy, "stray")
	mustSpillFile(t, dir, "hermex-imap")
	docs, err := readSpill(path)
	if err != nil || len(docs) != 1 || docs[0].(mongoDoc).Name != "before.upgrade" {
		t.Errorf("the daemon's spill holds %v (err %v), want its own before.upgrade event kept, not replaced", docs, err)
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
	// reconnect and drain the spill, the self-heal the user chose.
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

// TestSpillReplayHandlesCorruptAndTruncatedLine writes two valid spill lines
// followed by a truncated JSON record (as left by a crash mid-write) and verifies
// the two valid events are replayed without loss while the corrupt tail is ignored.
func TestSpillReplayHandlesCorruptAndTruncatedLine(t *testing.T) {
	spillPath := filepath.Join(t.TempDir(), "spill.jsonl")
	line1, err := json.Marshal(toDoc(Event{Time: time.Now().UTC(), Level: LevelInfo, Subsystem: IMAP, Name: "ok.1"}))
	mustNoErr(t, err, "marshal line 1")
	line2, err := json.Marshal(toDoc(Event{Time: time.Now().UTC(), Level: LevelInfo, Subsystem: IMAP, Name: "ok.2"}))
	mustNoErr(t, err, "marshal line 2")

	content := append(append(line1, '\n'), line2...)
	content = append(content, '\n')
	content = append(content, `{"ts":"2026-01-01T00:00:00Z","event":"truncated`...)
	mustNoErr(t, os.WriteFile(spillPath, content, 0o600), "write spill with corrupt tail")

	var got fakeInserter
	s := newAsyncSink(readyConn(&got), spillPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mustNoErr(t, s.Close(ctx), "Close")

	if _, total := got.totals(); total != 2 {
		t.Fatalf("replayed %d docs, want 2 valid events", total)
	}
	wantEq(t, got.docs[0].(mongoDoc).Name, "ok.1", "first replayed event")
	wantEq(t, got.docs[1].(mongoDoc).Name, "ok.2", "second replayed event")
	if spillFileHasData(spillPath) {
		t.Error("spill file was not removed after replaying valid records")
	}
}

// TestSpillToUnwritablePathKeepsStateUnchanged proves spilling to a path whose
// parent is a regular file does not set hasSpill and leaves the blocking file
// unmodified.
func TestSpillToUnwritablePathKeepsStateUnchanged(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	mustNoErr(t, os.WriteFile(blocker, []byte("keep"), 0o600), "write blocker")

	s := &MongoSink{spillPath: filepath.Join(blocker, "spill.jsonl")}
	s.spill([]Event{{Subsystem: IMAP, Name: "blocked"}})

	if s.hasSpill.Load() {
		t.Error("hasSpill became true even though opening the spill file failed")
	}
	raw, err := os.ReadFile(blocker)
	mustNoErr(t, err, "read blocker")
	wantEq(t, string(raw), "keep", "blocker file content")
}

// TestSpillNeverWritesRawPasswordToDisk verifies sensitive fields are redacted
// before reaching the spill file while the store is down.
func TestSpillNeverWritesRawPasswordToDisk(t *testing.T) {
	spillPath := filepath.Join(t.TempDir(), "spill.jsonl")
	down := inserterFunc(func(context.Context, []any) error { return errors.New("mongo down") })
	s := newAsyncSink(readyConn(down), spillPath)
	log := New(s)
	log.Info(Auth, "login", Fields{"password": "spill-secret-pw", "cookie": "sess=xyz"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mustNoErr(t, s.Close(ctx), "Close")

	raw, err := os.ReadFile(spillPath)
	mustNoErr(t, err, "read spill file")
	if bytes.Contains(raw, []byte("spill-secret-pw")) || bytes.Contains(raw, []byte("sess=xyz")) {
		t.Errorf("sensitive value leaked into spill file: %s", raw)
	}
	if !bytes.Contains(raw, []byte("[redacted]")) {
		t.Errorf("spill file missing [redacted] marker: %s", raw)
	}
}
