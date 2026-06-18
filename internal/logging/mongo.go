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

// MongoSink writes events to MongoDB in batches from one background goroutine.
// Write never blocks: when the buffer is full the event is dropped and counted,
// so logging can never stall a protocol hot path. It satisfies Sink.
type MongoSink struct {
	in      chan Event
	closing chan struct{}
	closed  chan struct{}
	dropped atomic.Uint64
	ins     inserter
	client  *mongo.Client // nil in unit tests that inject an inserter

	// Degradation: when an InsertMany fails the batch is appended to spillPath, and
	// the next successful write replays it. spillPath/hasSpill are touched only by
	// the single run() goroutine, so they need no synchronization.
	spillPath string
	hasSpill  bool
}

// NewMongoSink connects to uri, ensures the log collection's indexes (a TTL index
// enforcing the retention window plus indexes on the admin panel's filter keys),
// and starts the background writer. The log collection is "logs" in database.
// spillPath is the local file failed batches are appended to while MongoDB is
// unreachable (empty disables the spill — events are then dropped on failure).
func NewMongoSink(uri, database, spillPath string, retention time.Duration) (*MongoSink, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mongoWriteTimeout)
	defer cancel()
	if err := client.Ping(ctx, nil); err != nil {
		client.Disconnect(context.Background())
		return nil, err
	}
	coll := client.Database(database).Collection("logs")
	if err := ensureIndexes(ctx, coll, retention); err != nil {
		client.Disconnect(context.Background())
		return nil, err
	}
	s := newAsyncSink(collInserter{coll: coll}, spillPath)
	s.client = client
	return s, nil
}

// newAsyncSink starts the buffered writer over ins, spilling failed batches to
// spillPath. It is split out from NewMongoSink so the buffering and spill can be
// tested with a fake inserter. A spill file left by a previous run is replayed on
// the first successful write.
func newAsyncSink(ins inserter, spillPath string) *MongoSink {
	s := &MongoSink{
		in:        make(chan Event, mongoBufferSize),
		closing:   make(chan struct{}),
		closed:    make(chan struct{}),
		ins:       ins,
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
		if len(batch) == 0 {
			return
		}
		docs := make([]any, len(batch))
		for i, e := range batch {
			docs[i] = toDoc(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), mongoWriteTimeout)
		err := s.ins.InsertMany(ctx, docs)
		cancel()
		if err != nil {
			s.spill(batch) // MongoDB unreachable — preserve the batch on disk
		} else {
			s.replaySpill() // MongoDB is up — drain anything spilled earlier
		}
		batch = batch[:0]
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

// ensureIndexes creates the TTL index that ages out documents past the retention
// window and the compound indexes the admin panel filters on (subsystem, user,
// level — each paired with a descending time for recent-first scans).
func ensureIndexes(ctx context.Context, coll *mongo.Collection, retention time.Duration) error {
	ttl := int32(retention / time.Second)
	_, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "ts", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(ttl)},
		{Keys: bson.D{{Key: "subsystem", Value: 1}, {Key: "ts", Value: -1}}},
		{Keys: bson.D{{Key: "user", Value: 1}, {Key: "ts", Value: -1}}},
		{Keys: bson.D{{Key: "level", Value: 1}, {Key: "ts", Value: -1}}},
	})
	return err
}
