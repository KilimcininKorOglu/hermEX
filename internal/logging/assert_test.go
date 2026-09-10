package logging

import (
	"context"
	"os"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// This file is the package's assertion vocabulary plus the log-store fixtures the
// integration tests share. A test states one fact per call, so a failure names the
// fact that broke rather than the condition that evaluated.

// mustNoErr stops the test when a step failed.
func mustNoErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantEq fails the test unless the value equals what the caller expected.
func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// wantTrue fails the test unless the fact holds.
func wantTrue(t *testing.T, got bool, what string) {
	t.Helper()
	if !got {
		t.Errorf("%s is false, want true", what)
	}
}

// mustMongoURI returns the log store's URI, skipping the test when the dev
// container's mongo is absent (the host quick-feedback run).
func mustMongoURI(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("HERMEX_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("HERMEX_TEST_MONGO_URI not set (needs the dev container's mongo)")
	}
	return uri
}

// openTestMongo connects a raw client to the named database, dropping it before and
// after the test so each run starts from a clean slate.
func openTestMongo(t *testing.T, ctx context.Context, uri, db string) *mongo.Client {
	t.Helper()
	raw, err := mongo.Connect(options.Client().ApplyURI(uri))
	mustNoErr(t, err, "connect")
	t.Cleanup(func() {
		_ = raw.Database(db).Drop(ctx)
		_ = raw.Disconnect(ctx)
	})
	_ = raw.Database(db).Drop(ctx)
	return raw
}

// listIndexes reads a collection's index documents.
func listIndexes(t *testing.T, ctx context.Context, coll *mongo.Collection) []bson.M {
	t.Helper()
	cur, err := coll.Indexes().List(ctx)
	mustNoErr(t, err, "list indexes")
	var idx []bson.M
	mustNoErr(t, cur.All(ctx, &idx), "read the index list")
	return idx
}

// wantNoTTLIndex fails the test when any index expires documents: retention is
// enforced by the admin's pruning, so a TTL index would delete logs on a stale
// schedule the operator never chose.
func wantNoTTLIndex(t *testing.T, idx []bson.M, what string) {
	t.Helper()
	for _, m := range idx {
		if _, ok := m["expireAfterSeconds"]; ok {
			t.Errorf("%s carry a TTL index: %v", what, m)
		}
	}
}

// hasIndexNamed reports whether the index list holds one with that name.
func hasIndexNamed(idx []bson.M, name string) bool {
	for _, m := range idx {
		if got, _ := m["name"].(string); got == name {
			return true
		}
	}
	return false
}
