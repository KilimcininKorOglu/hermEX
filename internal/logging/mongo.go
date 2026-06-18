package logging

import (
	"context"
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
// at startup keeps buffering to disk and self-heals when the store comes up —
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
	ins     inserter      // run()-owned; nil until the first successful connect
	client  *mongo.Client // nil in unit tests that inject a connector; Close disconnects it

	// Degradation: when a write fails, or the store is not yet reachable, the batch
	// is appended to spillPath and a later flush replays it. spillPath/hasSpill are
	// touched only by the single run() goroutine, so they need no synchronization.
	spillPath string
	hasSpill  bool
}

// NewMongoSink prepares a sink for the log collection "logs" in database at uri and
// starts the background writer. It returns an error only for a permanently broken
// (malformed) URI — a store that is merely unreachable is NOT an error: mongo.Connect
// is lazy, so the sink starts, spills to disk, and connects (Ping + index creation)
// on the first flush that finds the store up. This keeps a daemon serving even when
// the log store is down at boot. spillPath is the local file batches are appended to
// while the store is unreachable (empty disables the spill — events drop on failure).
func NewMongoSink(uri, database, spillPath string, retention time.Duration) (*MongoSink, error) {
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
		if err := ensureIndexes(ctx, coll, retention); err != nil {
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
	}
	s.hasSpill = spillFileHasData(spillPath)
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

// run is the single writer goroutine: it batches incoming events and flushes on a
// full batch, on a tick, and once more while draining at shutdown.
func (s *MongoSink) run() {
	defer close(s.closed)
	t := time.NewTicker(mongoFlushInterval)
	defer t.Stop()
	batch := make([]Event, 0, mongoBatchSize)

	flush := func() {
		// Nothing buffered and nothing spilled — no reason to touch the store.
		if len(batch) == 0 && !s.hasSpill {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), mongoWriteTimeout)
		defer cancel()
		// Lazily open the connection; while the store is down this fails, so the
		// batch is spilled and the connection is retried on the next flush.
		if s.ins == nil {
			ins, err := s.connect(ctx)
			if err != nil {
				if len(batch) > 0 {
					s.spill(batch)
					batch = batch[:0]
				}
				return
			}
			s.ins = ins
		}
		if len(batch) > 0 {
			docs := make([]any, len(batch))
			for i, e := range batch {
				docs[i] = toDoc(e)
			}
			if err := s.ins.InsertMany(ctx, docs); err != nil {
				s.spill(batch) // transient write failure — preserve and replay later
				batch = batch[:0]
				return
			}
			batch = batch[:0]
		}
		s.replaySpill() // connected and caught up — drain anything spilled earlier
	}

	for {
		select {
		case e := <-s.in:
			batch = append(batch, e)
			if len(batch) >= mongoBatchSize {
				flush()
			}
		case <-t.C:
			flush()
		case <-s.closing:
			// Drain everything still buffered, then make a final flush.
			for {
				select {
				case e := <-s.in:
					batch = append(batch, e)
					if len(batch) >= mongoBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
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
// (subsystem, user, level — each paired with a descending time for recent-first
// scans) and, for a positive retention, a TTL index that ages out old documents.
// A zero or negative retention means keep forever: it must NOT become
// SetExpireAfterSeconds(0), which MongoDB would treat as "expire immediately" and
// delete every log within a minute.
func ensureIndexes(ctx context.Context, coll *mongo.Collection, retention time.Duration) error {
	models := []mongo.IndexModel{
		{Keys: bson.D{{Key: "subsystem", Value: 1}, {Key: "ts", Value: -1}}},
		{Keys: bson.D{{Key: "user", Value: 1}, {Key: "ts", Value: -1}}},
		{Keys: bson.D{{Key: "level", Value: 1}, {Key: "ts", Value: -1}}},
	}
	if retention > 0 {
		models = append(models, mongo.IndexModel{
			Keys:    bson.D{{Key: "ts", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(int32(retention / time.Second)),
		})
	}
	_, err := coll.Indexes().CreateMany(ctx, models)
	return err
}
