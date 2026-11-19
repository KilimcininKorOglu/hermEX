package logging

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	mongoBufferSize    = 4096             // events held before Write starts dropping
	mongoBatchSize     = 256              // most events written in one InsertMany
	mongoFlushInterval = time.Second      // flush a partial batch at least this often
	mongoWriteTimeout  = 10 * time.Second // per connect/insert deadline
	// storeRetryInterval is how long the writer spills straight to disk after the
	// store failed, rather than waiting a whole write deadline on it again for every
	// batch. Waiting held the single writer, the buffer filled, and events were
	// dropped instead of spilled.
	storeRetryInterval = 5 * time.Second
)

// inserter is the slice of a Mongo collection the async writer needs, so the
// buffering and batching can be tested without a live server.
type inserter interface {
	InsertMany(ctx context.Context, docs []any) error
}

// collInserter adapts a *mongo.Collection to inserter (InsertMany returns a result
// the sink ignores).
type collInserter struct{ coll *mongo.Collection }

func (c collInserter) InsertMany(ctx context.Context, docs []any) error {
	_, err := c.coll.InsertMany(ctx, docs)
	return err
}

// connector establishes (and validates) the connection to the log store and
// returns the inserter to write through. The writer goroutine calls it lazily and
// retries it on every flush until it first succeeds, so a sink whose store is down
// at startup keeps buffering to disk and self-heals when the store comes up,
// logging never blocks a daemon, and no events are lost to a store that is merely
// late to start.
type connector func(ctx context.Context) (inserter, error)

// MongoSink writes events to MongoDB in batches from one background goroutine.
// Write never blocks: when the buffer is full the event is dropped and counted,
// so logging can never stall a protocol hot path. It satisfies Sink.
type MongoSink struct {
	in      chan Event
	closing chan struct{}
	closed  chan struct{}
	dropped atomic.Uint64
	connect connector     // opens/validates the store connection; retried until it succeeds
	ins     inserter      // storeLoop-owned; nil until the first successful connect
	client  *mongo.Client // nil in unit tests that inject a connector; Close disconnects it

	// toStore carries batches from run to storeLoop; storeDone closes when
	// storeLoop has written the last one.
	toStore   chan []Event
	storeDone chan struct{}

	// Degradation: when a write fails, the store is not yet reachable, or storeLoop
	// is still busy, the batch is appended to spillPath and a later write replays
	// it. Both goroutines spill, so spillMu orders their appends and the replay's
	// hand-off of the file.
	spillPath string
	spillMu   sync.Mutex
	hasSpill  atomic.Bool
	// spillLimit caps the spill file in bytes (0: no cap), so a long store outage
	// cannot fill the disk the mailboxes live on. It is set from the operator's
	// setting while the sink runs, hence atomic. spillDropped counts the events
	// the cap turned away.
	spillLimit   atomic.Int64
	spillDropped atomic.Uint64

	// writeTimeout bounds each connect and insert (mongoWriteTimeout; a test
	// shortens it). downUntil is when the store may be tried again after a
	// failure (storeLoop only), and reportedDrops how many buffer drops stderr
	// has been told of (run only).
	writeTimeout  atomic.Int64
	downUntil     time.Time
	reportedDrops uint64
}

// SetSpillLimit caps the spill file at limit bytes; zero or negative removes the
// cap. It takes effect on the next spill, without a restart.
func (s *MongoSink) SetSpillLimit(limit int64) { s.spillLimit.Store(max(limit, 0)) }

// SpillDropped reports how many events the spill cap turned away.
func (s *MongoSink) SpillDropped() uint64 { return s.spillDropped.Load() }

// NewMongoSink prepares a sink for the log collection "logs" in database at uri and
// starts the background writer. It returns an error only for a permanently broken
// (malformed) URI, a store that is merely unreachable is NOT an error: mongo.Connect
// is lazy, so the sink starts, spills to disk, and connects (Ping + index creation)
// on the first flush that finds the store up. This keeps a daemon serving even when
// the log store is down at boot. spillPath is the local file batches are appended to
// while the store is unreachable (empty disables the spill, events drop on failure).
func NewMongoSink(uri, database, spillPath string) (*MongoSink, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	coll := client.Database(database).Collection("logs")
	// Ping and index creation are deferred to the writer goroutine so a store that
	// is down at startup does not fail construction. ensureIndexes is idempotent, so
	// running it on the first reachable flush is safe; every daemon logs a startup
	// event, so the indexes are created promptly in practice.
	connect := func(ctx context.Context) (inserter, error) {
		if err := client.Ping(ctx, nil); err != nil {
			return nil, err
		}
		if err := ensureIndexes(ctx, coll); err != nil {
			return nil, err
		}
		return collInserter{coll: coll}, nil
	}
	s := newAsyncSink(connect, spillPath)
	s.client = client
	return s, nil
}

// newAsyncSink starts the buffered writer that connects through connect and spills
// failed batches to spillPath. It is split out from NewMongoSink so the buffering,
// reconnection, and spill can be tested with a fake connector. A spill file left by
// a previous run is replayed on the first flush that reaches the store.
func newAsyncSink(connect connector, spillPath string) *MongoSink {
	s := &MongoSink{
		in:        make(chan Event, mongoBufferSize),
		closing:   make(chan struct{}),
		closed:    make(chan struct{}),
		connect:   connect,
		spillPath: spillPath,
		toStore:   make(chan []Event, 1),
		storeDone: make(chan struct{}),
	}
	s.hasSpill.Store(spillFileHasData(spillPath) || spillFileHasData(replayPath(spillPath)))
	s.writeTimeout.Store(int64(mongoWriteTimeout))
	go s.run()
	return s
}

// Write enqueues e without ever blocking; a full buffer drops and counts it.
func (s *MongoSink) Write(e Event) {
	select {
	case s.in <- e:
	default:
		s.dropped.Add(1)
	}
}

// Dropped reports how many events were dropped because the buffer was full.
func (s *MongoSink) Dropped() uint64 { return s.dropped.Load() }

// run is the batching goroutine: it batches incoming events and flushes on a full
// batch, on a tick, and once more while draining at shutdown. It hands batches to
// the store goroutine and never waits on the store itself, so a store that hangs
// until its write deadline cannot back the buffer up into dropped events.
func (s *MongoSink) run() {
	go s.storeLoop()
	defer func() {
		close(s.toStore)
		<-s.storeDone
		close(s.closed)
	}()
	t := time.NewTicker(mongoFlushInterval)
	defer t.Stop()
	batch := make([]Event, 0, mongoBatchSize)

	for {
		select {
		case e := <-s.in:
			s.buffer(&batch, e)
		case <-t.C:
			s.flush(&batch)
		case <-s.closing:
			s.drain(&batch)
			return
		}
	}
}

// buffer appends one event, flushing once the batch is full.
func (s *MongoSink) buffer(batch *[]Event, e Event) {
	*batch = append(*batch, e)
	if len(*batch) >= mongoBatchSize {
		s.flush(batch)
	}
}

// drain empties everything still buffered at shutdown, then makes a final flush.
func (s *MongoSink) drain(batch *[]Event) {
	for {
		select {
		case e := <-s.in:
			s.buffer(batch, e)
		default:
			s.flush(batch)
			return
		}
	}
}

// flush hands the buffered batch to the store goroutine, or spills it when that
// goroutine is still busy with an earlier batch (a slow or hanging store).
func (s *MongoSink) flush(batch *[]Event) {
	s.reportDrops()
	if len(*batch) == 0 {
		return
	}
	b := slices.Clone(*batch)
	*batch = (*batch)[:0]
	select {
	case s.toStore <- b:
	default:
		s.spill(b)
	}
}

// storeLoop is the only goroutine that talks to the store. It writes each batch
// it is handed and, on a tick, replays a spill once the store is back.
func (s *MongoSink) storeLoop() {
	defer close(s.storeDone)
	t := time.NewTicker(mongoFlushInterval)
	defer t.Stop()
	for {
		select {
		case b, ok := <-s.toStore:
			if !ok {
				// Shutting down: one last pass drains a spill, as the old final
				// flush did, so a sink that wrote nothing still replays it.
				if s.hasSpill.Load() {
					s.store(nil)
				}
				return
			}
			s.store(b)
		case <-t.C:
			if s.hasSpill.Load() {
				s.store(nil)
			}
		}
	}
}

// store writes batch (which may be empty) and then replays anything spilled. A
// failure spills the batch and keeps the store untried for storeRetryInterval.
func (s *MongoSink) store(batch []Event) {
	if time.Now().Before(s.downUntil) {
		s.spill(batch)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.writeTimeout.Load()))
	defer cancel()
	if !s.ensureConnected(ctx) || !s.insertBatch(ctx, batch) {
		s.spill(batch)
		s.downUntil = time.Now().Add(storeRetryInterval)
		return
	}
	// Connected and caught up: drain anything spilled earlier. A replay that fails
	// found the store down, so it backs off like a failed write.
	if !s.replaySpill() {
		s.downUntil = time.Now().Add(storeRetryInterval)
	}
}

// reportDrops tells stderr how many events the full buffer turned away since the
// last report, so a loss is never silent.
func (s *MongoSink) reportDrops() {
	d := s.dropped.Load()
	if d == s.reportedDrops {
		return
	}
	fmt.Fprintf(os.Stderr, "logging: %d events dropped, the log buffer was full\n", d-s.reportedDrops)
	s.reportedDrops = d
}

// ensureConnected lazily opens the connection. While the store is down this fails
// and the caller spills; the connection is retried after storeRetryInterval.
func (s *MongoSink) ensureConnected(ctx context.Context) bool {
	if s.ins != nil {
		return true
	}
	ins, err := s.connect(ctx)
	if err != nil {
		return false
	}
	s.ins = ins
	return true
}

// insertBatch writes the events; false means the caller must spill them.
func (s *MongoSink) insertBatch(ctx context.Context, batch []Event) bool {
	if len(batch) == 0 {
		return true
	}
	docs := make([]any, len(batch))
	for i, e := range batch {
		docs[i] = toDoc(e)
	}
	return s.ins.InsertMany(ctx, docs) == nil
}

// Close stops the writer (flushing buffered events) and disconnects the client,
// honoring ctx's deadline for the drain.
func (s *MongoSink) Close(ctx context.Context) error {
	close(s.closing)
	select {
	case <-s.closed:
	case <-ctx.Done():
	}
	if s.client != nil {
		return s.client.Disconnect(ctx)
	}
	return nil
}

// mongoDoc is the stored shape of an Event. The indexed columns are always
// present; the rest are omitted when empty.
type mongoDoc struct {
	Time       time.Time      `bson:"ts"`
	Level      string         `bson:"level"`
	Subsystem  string         `bson:"subsystem"`
	Name       string         `bson:"event"`
	User       string         `bson:"user,omitempty"`
	RemoteAddr string         `bson:"remote_addr,omitempty"`
	RequestID  string         `bson:"request_id,omitempty"`
	DurationMs int64          `bson:"duration_ms,omitempty"`
	Fields     map[string]any `bson:"fields,omitempty"`
	Err        string         `bson:"err,omitempty"`
}

func toDoc(e Event) mongoDoc {
	return mongoDoc{
		Time:       e.Time,
		Level:      e.Level.String(),
		Subsystem:  string(e.Subsystem),
		Name:       e.Name,
		User:       e.User,
		RemoteAddr: e.RemoteAddr,
		RequestID:  e.RequestID,
		DurationMs: e.DurationMs,
		Fields:     e.Fields,
		Err:        e.Err,
	}
}

// ensureIndexes creates the compound indexes the admin panel filters on
// (subsystem, user, level, each paired with a descending time for recent-first
// scans). It deliberately creates NO TTL index: retention is enforced dynamically by
// the admin daemon, which prunes the collection to the operator-set window so the
// window can change at runtime. A TTL index here would override that with its stale
// startup value, so it is gone, see Reader.PruneOlderThan and DropLegacyTTLIndex.
func ensureIndexes(ctx context.Context, coll *mongo.Collection) error {
	models := []mongo.IndexModel{
		{Keys: bson.D{{Key: "subsystem", Value: 1}, {Key: "ts", Value: -1}}},
		{Keys: bson.D{{Key: "user", Value: 1}, {Key: "ts", Value: -1}}},
		{Keys: bson.D{{Key: "level", Value: 1}, {Key: "ts", Value: -1}}},
	}
	_, err := coll.Indexes().CreateMany(ctx, models)
	return err
}
