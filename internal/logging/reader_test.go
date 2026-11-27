package logging

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// TestReaderPruneOlderThan proves the retention prune deletes only events older than
// the cutoff and leaves newer ones, the operation the admin runs to enforce the
// retention window without a TTL index. Skips without the dev container's mongo.
func TestReaderPruneOlderThan(t *testing.T) {
	uri := mustMongoURI(t)
	const db = "hermex_logprunetest"
	bg := context.Background()
	raw := openTestMongo(t, bg, uri, db)

	now := time.Now().UTC()
	coll := raw.Database(db).Collection("logs")
	if _, err := coll.InsertMany(bg, []any{
		bson.M{"ts": now.Add(-100 * 24 * time.Hour), "event": "old"},
		bson.M{"ts": now.Add(-40 * 24 * time.Hour), "event": "older"},
		bson.M{"ts": now.Add(-1 * 24 * time.Hour), "event": "recent"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reader, err := NewReader(uri, db)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close reader: %v", err)
		}
	}()

	cutoff := now.Add(-30 * 24 * time.Hour)
	n, err := reader.PruneOlderThan(bg, cutoff)
	if err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned %d, want 2 (the two events older than 30 days)", n)
	}
	remaining, err := coll.CountDocuments(bg, bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Errorf("%d events remain, want 1 (only the recent one)", remaining)
	}
}

// TestReaderDropLegacyTTLIndex proves a leftover TTL index from an earlier build is
// removed while the ordinary indexes survive, so a stale window cannot override the
// operator's pruning-based retention. Skips without the dev container's mongo.
func TestReaderDropLegacyTTLIndex(t *testing.T) {
	uri := mustMongoURI(t)
	const db = "hermex_logttldroptest"
	bg := context.Background()
	raw := openTestMongo(t, bg, uri, db)

	coll := raw.Database(db).Collection("logs")
	// A legacy TTL index plus an ordinary filter index, the way an older build left it.
	_, err := coll.Indexes().CreateMany(bg, []mongo.IndexModel{
		{Keys: bson.D{{Key: "ts", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(86400)},
		{Keys: bson.D{{Key: "subsystem", Value: 1}, {Key: "ts", Value: -1}}},
	})
	mustNoErr(t, err, "seed indexes")

	reader, err := NewReader(uri, db)
	mustNoErr(t, err, "NewReader")
	defer func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close reader: %v", err)
		}
	}()

	mustNoErr(t, reader.DropLegacyTTLIndex(bg), "DropLegacyTTLIndex")
	// Idempotent: a second call with no TTL index left must also succeed.
	mustNoErr(t, reader.DropLegacyTTLIndex(bg), "DropLegacyTTLIndex (second call)")

	idx := listIndexes(t, bg, coll)
	wantNoTTLIndex(t, idx, "the indexes after the drop")
	wantTrue(t, hasIndexNamed(idx, "subsystem_1_ts_-1"), "the ordinary subsystem index survived the drop")
}

// TestNewReaderRefusesMalformedURIAndDefaultsDatabase checks NewReader without a
// live Mongo server: a malformed URI is rejected, and an empty database name
// defaults to "hermex" targeting the "logs" collection.
func TestNewReaderRefusesMalformedURIAndDefaultsDatabase(t *testing.T) {
	bad, err := NewReader("http://invalid", "db")
	if err == nil {
		t.Fatal("NewReader accepted a malformed URI")
	}
	if bad != nil {
		t.Error("NewReader returned a non-nil reader on error")
	}

	r, err := NewReader("mongodb://127.0.0.1:27017", "")
	mustNoErr(t, err, "NewReader with valid URI and default database")
	defer func() {
		if err := r.Close(); err != nil {
			t.Errorf("close reader: %v", err)
		}
	}()
	wantEq(t, r.coll.Database().Name(), "hermex", "default database name")
	wantEq(t, r.coll.Name(), "logs", "log collection name")
}
