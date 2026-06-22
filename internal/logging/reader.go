package logging

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// LogEntry is one stored log event, projected for the admin log viewer.
type LogEntry struct {
	Time       time.Time `bson:"ts"`
	Level      string    `bson:"level"`
	Subsystem  string    `bson:"subsystem"`
	Name       string    `bson:"event"`
	User       string    `bson:"user"`
	RemoteAddr string    `bson:"remote_addr"`
	Err        string    `bson:"err"`
}

// Reader queries the central log store for the admin viewer.
type Reader struct {
	client *mongo.Client
	coll   *mongo.Collection
}

// NewReader connects to the "logs" collection in database at uri (database
// defaults to "hermex"). Like the sink, the connection is lazy: an unreachable
// store surfaces only when a query runs.
func NewReader(uri, database string) (*Reader, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	if database == "" {
		database = "hermex"
	}
	return &Reader{client: client, coll: client.Database(database).Collection("logs")}, nil
}

// Recent returns the newest log events, most recent first, up to limit, optionally
// filtered to a single subsystem (empty matches all subsystems).
func (r *Reader) Recent(ctx context.Context, subsystem string, limit int64) ([]LogEntry, error) {
	filter := bson.D{}
	if subsystem != "" {
		filter = bson.D{{Key: "subsystem", Value: subsystem}}
	}
	cur, err := r.coll.Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "ts", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var entries []LogEntry
	for cur.Next(ctx) {
		var e LogEntry
		if err := cur.Decode(&e); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, cur.Err()
}

// PruneOlderThan deletes every stored log event whose timestamp is before cutoff and
// returns how many were removed. This is how the admin enforces the operator's
// retention window without a Mongo TTL index, so the window can be changed at runtime;
// the caller is responsible for never pruning when retention is "keep forever".
func (r *Reader) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.coll.DeleteMany(ctx, bson.M{"ts": bson.M{"$lt": cutoff}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// DropLegacyTTLIndex removes any TTL index (one carrying expireAfterSeconds) from the
// logs collection. Earlier builds created such an index from the static retention
// config; retention is now enforced by PruneOlderThan, so a leftover TTL would silently
// delete logs on its own stale schedule and override the operator's window. Dropping it
// is idempotent — a collection with no TTL index is left unchanged.
func (r *Reader) DropLegacyTTLIndex(ctx context.Context) error {
	cur, err := r.coll.Indexes().List(ctx)
	if err != nil {
		return err
	}
	var idx []bson.M
	if err := cur.All(ctx, &idx); err != nil {
		return err
	}
	for _, m := range idx {
		if _, ok := m["expireAfterSeconds"]; !ok {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		if err := r.coll.Indexes().DropOne(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// Close disconnects from the log store.
func (r *Reader) Close() error {
	return r.client.Disconnect(context.Background())
}
