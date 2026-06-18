package logging

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// inserterFunc adapts a function to inserter.
type inserterFunc func(context.Context, []any) error

func (f inserterFunc) InsertMany(ctx context.Context, docs []any) error { return f(ctx, docs) }

// fakeInserter records every document handed to InsertMany.
type fakeInserter struct {
	mu    sync.Mutex
	docs  []any
	calls int
}

func (f *fakeInserter) InsertMany(_ context.Context, docs []any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.docs = append(f.docs, docs...)
	f.calls++
	return nil
}

func (f *fakeInserter) totals() (calls, docs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, len(f.docs)
}

// TestMongoSinkFlushesBufferedEventsOnClose proves every enqueued event reaches
// the inserter — Close drains the buffer and makes a final flush.
func TestMongoSinkFlushesBufferedEventsOnClose(t *testing.T) {
	fi := &fakeInserter{}
	s := newAsyncSink(fi, "")
	const n = 10
	for range n {
		s.Write(Event{Subsystem: IMAP, Name: "conn.accept"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	calls, docs := fi.totals()
	if docs != n {
		t.Errorf("wrote %d docs, want %d (events lost on flush)", docs, n)
	}
	if calls == 0 {
		t.Error("InsertMany was never called")
	}
}

// TestMongoSinkWriteNeverBlocks proves Write drops and counts events instead of
// blocking when the writer is stalled and the buffer fills — logging must never
// stall a protocol hot path.
func TestMongoSinkWriteNeverBlocks(t *testing.T) {
	block := make(chan struct{})
	stalled := inserterFunc(func(_ context.Context, _ []any) error {
		<-block
		return nil
	})
	s := newAsyncSink(stalled, "")

	const flood = mongoBufferSize + mongoBatchSize + 5000
	done := make(chan struct{})
	go func() {
		for range flood {
			s.Write(Event{Subsystem: System, Name: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked: it did not return for every event within 5s")
	}
	if s.Dropped() == 0 {
		t.Error("expected events to be dropped once the buffer filled")
	}
	close(block) // let the writer drain so Close can finish
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Close(ctx)
}

// TestMongoSinkIntegration drives a real MongoDB (the dev container's mongo): it
// writes an event through NewMongoSink, reads it back, and confirms the stored
// shape and that the TTL plus filter indexes were created. It skips without the
// env, so the host quick-feedback run is unaffected.
func TestMongoSinkIntegration(t *testing.T) {
	uri := os.Getenv("HERMEX_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("HERMEX_TEST_MONGO_URI not set (needs the dev container's mongo)")
	}
	const db = "hermex_logtest"
	bg := context.Background()

	raw, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer raw.Disconnect(bg)
	raw.Database(db).Drop(bg) // clean slate
	defer raw.Database(db).Drop(bg)

	sink, err := NewMongoSink(uri, db, "", time.Hour)
	if err != nil {
		t.Fatalf("NewMongoSink: %v", err)
	}
	sink.Write(Event{
		Time: time.Now().UTC(), Level: LevelInfo, Subsystem: IMAP, Name: "auth.ok",
		User: "alice@hermex.test", RemoteAddr: "10.0.0.1", Fields: Fields{"folder": "INBOX"},
	})
	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()
	if err := sink.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	coll := raw.Database(db).Collection("logs")
	var got mongoDoc
	if err := coll.FindOne(ctx, bson.D{{Key: "event", Value: "auth.ok"}}).Decode(&got); err != nil {
		t.Fatalf("read the event back: %v", err)
	}
	if got.User != "alice@hermex.test" || got.Subsystem != "imap" || got.Level != "info" {
		t.Errorf("stored doc = %+v, want imap/info/alice", got)
	}
	if got.Fields["folder"] != "INBOX" {
		t.Errorf("stored fields = %v, want folder=INBOX", got.Fields)
	}

	cur, err := coll.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	var idx []bson.M
	if err := cur.All(ctx, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx) < 5 {
		t.Errorf("got %d indexes, want >= 5 (ttl + 3 filter indexes + _id)", len(idx))
	}
	hasTTL := false
	for _, m := range idx {
		if _, ok := m["expireAfterSeconds"]; ok {
			hasTTL = true
		}
	}
	if !hasTTL {
		t.Error("no TTL index (expireAfterSeconds) was created")
	}
}

// TestMongoSinkNoTTLWhenRetentionZero proves the keep-forever guard: a zero
// retention must NOT create a TTL index. Without the guard ensureIndexes would
// call SetExpireAfterSeconds(0), which MongoDB treats as "expire immediately" —
// silently deleting every log within a minute and destroying the audit trail.
func TestMongoSinkNoTTLWhenRetentionZero(t *testing.T) {
	uri := os.Getenv("HERMEX_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("HERMEX_TEST_MONGO_URI not set (needs the dev container's mongo)")
	}
	const db = "hermex_logttltest"
	bg := context.Background()

	raw, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer raw.Disconnect(bg)
	raw.Database(db).Drop(bg)
	defer raw.Database(db).Drop(bg)

	sink, err := NewMongoSink(uri, db, "", 0)
	if err != nil {
		t.Fatalf("NewMongoSink: %v", err)
	}
	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()
	if err := sink.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cur, err := raw.Database(db).Collection("logs").Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	var idx []bson.M
	if err := cur.All(ctx, &idx); err != nil {
		t.Fatal(err)
	}
	for _, m := range idx {
		if _, ok := m["expireAfterSeconds"]; ok {
			t.Fatalf("a TTL index was created for retention 0: %v — logs would expire immediately", m)
		}
	}
	if len(idx) < 4 {
		t.Errorf("got %d indexes, want >= 4 (3 filter indexes + _id, no TTL)", len(idx))
	}
}
