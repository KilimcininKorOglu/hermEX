package logging

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// TestReaderPruneOlderThan proves the retention prune deletes only events older than
// the cutoff and leaves newer ones — the operation the admin runs to enforce the
// retention window without a TTL index. Skips without the dev container's mongo.
func TestReaderPruneOlderThan(t *testing.T) {
	uri := os.Getenv("HERMEX_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("HERMEX_TEST_MONGO_URI not set (needs the dev container's mongo)")
	}
	const db = "hermex_logprunetest"
	bg := context.Background()

	raw, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer raw.Disconnect(bg)
	raw.Database(db).Drop(bg)
	defer raw.Database(db).Drop(bg)

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
	defer reader.Close()

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
	uri := os.Getenv("HERMEX_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("HERMEX_TEST_MONGO_URI not set (needs the dev container's mongo)")
	}
	const db = "hermex_logttldroptest"
	bg := context.Background()

	raw, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer raw.Disconnect(bg)
	raw.Database(db).Drop(bg)
	defer raw.Database(db).Drop(bg)

	coll := raw.Database(db).Collection("logs")
	// A legacy TTL index plus an ordinary filter index, the way an older build left it.
	if _, err := coll.Indexes().CreateMany(bg, []mongo.IndexModel{
		{Keys: bson.D{{Key: "ts", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(86400)},
		{Keys: bson.D{{Key: "subsystem", Value: 1}, {Key: "ts", Value: -1}}},
	}); err != nil {
		t.Fatalf("seed indexes: %v", err)
	}

	reader, err := NewReader(uri, db)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer reader.Close()

	if err := reader.DropLegacyTTLIndex(bg); err != nil {
		t.Fatalf("DropLegacyTTLIndex: %v", err)
	}
	// Idempotent: a second call with no TTL index left must also succeed.
	if err := reader.DropLegacyTTLIndex(bg); err != nil {
		t.Fatalf("DropLegacyTTLIndex (second call): %v", err)
	}

	cur, err := coll.Indexes().List(bg)
	if err != nil {
		t.Fatal(err)
	}
	var idx []bson.M
	if err := cur.All(bg, &idx); err != nil {
		t.Fatal(err)
	}
	hasSubsystem := false
	for _, m := range idx {
		if _, ok := m["expireAfterSeconds"]; ok {
			t.Errorf("a TTL index survived the drop: %v", m)
		}
		if name, _ := m["name"].(string); name == "subsystem_1_ts_-1" {
			hasSubsystem = true
		}
	}
	if !hasSubsystem {
		t.Errorf("the ordinary subsystem index was dropped; indexes = %v", idx)
	}
}
