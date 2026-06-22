package logging_test

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"hermex/internal/logging"
)

// TestMultiSinkFansOut proves an event reaches every member sink and that a nil
// member is skipped rather than panicking.
func TestMultiSinkFansOut(t *testing.T) {
	a, b := &capture{}, &capture{}
	m := logging.NewMultiSink(a, nil, b)
	m.Write(logging.Event{Subsystem: logging.IMAP, Name: "x"})
	if len(a.events) != 1 || len(b.events) != 1 {
		t.Fatalf("fan-out delivered a=%d b=%d, want 1 each", len(a.events), len(b.events))
	}
}

// TestBuildStderrOnlyWhenNoMongo proves Build returns a working stderr logger when
// no Mongo URI is configured.
func TestBuildStderrOnlyWhenNoMongo(t *testing.T) {
	log, closeFn := logging.Build("", "db", "")
	if log == nil {
		t.Fatal("Build returned a nil logger")
	}
	log.Info(logging.System, "startup", nil) // must not panic
	if err := closeFn(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestBuildFallsBackOnBadURI proves logging never blocks a daemon from starting: a
// permanently broken (malformed) Mongo URI yields a working stderr logger, not an
// error. A valid-but-unreachable URI is different — that sink is created and
// self-heals (see TestConnectFailureSpillsThenRecovers), so it does not fall back.
func TestBuildFallsBackOnBadURI(t *testing.T) {
	log, closeFn := logging.Build("http://invalid", "db", "")
	if log == nil {
		t.Fatal("Build returned a nil logger for a malformed Mongo URI")
	}
	log.Info(logging.System, "startup", nil) // stderr path, must not panic
	if err := closeFn(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestBuildIntegration proves Build wires the Mongo sink: an event logged through
// the built logger lands in MongoDB. Skips without the dev container's mongo.
func TestBuildIntegration(t *testing.T) {
	uri := os.Getenv("HERMEX_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("HERMEX_TEST_MONGO_URI not set (needs the dev container's mongo)")
	}
	const db = "hermex_logbuildtest"
	bg := context.Background()

	raw, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer raw.Disconnect(bg)
	raw.Database(db).Drop(bg)
	defer raw.Database(db).Drop(bg)

	log, closeFn := logging.Build(uri, db, t.TempDir())
	log.Info(logging.System, "startup", logging.Fields{"daemon": "test"})
	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	n, err := raw.Database(db).Collection("logs").CountDocuments(ctx, bson.D{{Key: "event", Value: "startup"}})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("startup events in Mongo = %d, want 1 (Build did not wire the Mongo sink)", n)
	}
}
