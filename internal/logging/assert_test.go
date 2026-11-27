package logging

import (
	"context"
	"errors"
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
		if err := raw.Database(db).Drop(ctx); err != nil {
			t.Errorf("drop test database: %v", err)
		}
		if err := raw.Disconnect(ctx); err != nil {
			t.Errorf("disconnect test mongo: %v", err)
		}
	})
	mustNoErr(t, raw.Database(db).Drop(ctx), "drop the leftover test database")
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
		if got, ok := m["name"].(string); ok && got == name {
			return true
		}
	}
	return false
}

// sinkFunc adapts a function to Sink for small internal tests.
type sinkFunc func(Event)

func (f sinkFunc) Write(e Event) { f(e) }

// TestSettingsReadFailedRecordsErrorAndFields verifies SettingsReadFailed emits a
// warn event under System with daemon, settings family, detail, and optional error.
func TestSettingsReadFailedRecordsErrorAndFields(t *testing.T) {
	var events []Event
	l := New(sinkFunc(func(e Event) { events = append(events, e) }))

	SettingsReadFailed(l, "hermex-imap", "log-spill-limit", "keeping prior cap", errors.New("db down"))
	SettingsReadFailed(l, "hermex-imap", "log-spill-limit", "keeping prior cap", nil)

	wantEq(t, len(events), 2, "recorded settings failure events")
	wantEq(t, events[0].Level, LevelWarn, "event level")
	wantEq(t, events[0].Subsystem, System, "event subsystem")
	wantEq(t, events[0].Name, "settings.read.fail", "event name")
	wantEq(t, events[0].Fields["daemon"], any("hermex-imap"), "daemon field")
	wantEq(t, events[0].Fields["settings"], any("log-spill-limit"), "settings field")
	wantEq(t, events[0].Fields["detail"], any("keeping prior cap"), "detail field")
	wantEq(t, events[0].Fields["error"], any("db down"), "error field")
	_, hasErr := events[1].Fields["error"]
	wantEq(t, hasErr, false, "error field present when err is nil")
}
