package logging

import (
	"context"
	"path/filepath"
	"time"
)

// logFlushTimeout bounds the final flush + disconnect the close function performs
// at shutdown. lifecycle.Run hands cleanups no context, so the deadline is baked
// in here rather than supplied by the caller.
const logFlushTimeout = 10 * time.Second

// MultiSink fans each event out to several sinks — e.g. stderr for operator
// visibility plus MongoDB for the queryable store. A member's Write is best
// effort; one slow or failing sink does not stop the others.
type MultiSink struct{ sinks []Sink }

// NewMultiSink returns a MultiSink over the non-nil sinks.
func NewMultiSink(sinks ...Sink) *MultiSink {
	out := make([]Sink, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			out = append(out, s)
		}
	}
	return &MultiSink{sinks: out}
}

// Write delivers e to every member sink.
func (m *MultiSink) Write(e Event) {
	for _, s := range m.sinks {
		s.Write(e)
	}
}

// Build assembles the process logger from configuration. It always writes to
// stderr for operator visibility, and additionally to MongoDB when mongoURI is
// set. Logging must never stop a daemon from starting, so if the Mongo sink
// cannot be created (server unreachable, bad URI) Build logs a warning to stderr
// and returns a stderr-only logger rather than an error. It returns the logger
// and a close function (flush + disconnect) to run as a shutdown cleanup; it has
// the func() error shape lifecycle.Run expects and bounds its own flush deadline.
//
// database is the Mongo database holding the logs collection (defaults to
// "hermex" when empty); spillDir, when set, is where failed batches are buffered
// while Mongo is unreachable; retentionDays is the TTL window in days — zero or
// negative keeps logs forever (no TTL index), which is the safe default for an
// unset value rather than expiring everything immediately.
func Build(mongoURI, database, spillDir string, retentionDays int) (*Logger, func() error) {
	stderr := NewStderrSink(nil)
	noop := func() error { return nil }
	if mongoURI == "" {
		return New(stderr), noop
	}
	if database == "" {
		database = "hermex"
	}

	spillPath := ""
	if spillDir != "" {
		spillPath = filepath.Join(spillDir, "logspill.jsonl")
	}
	var retention time.Duration
	if retentionDays > 0 {
		retention = time.Duration(retentionDays) * 24 * time.Hour
	}
	ms, err := NewMongoSink(mongoURI, database, spillPath, retention)
	if err != nil {
		stderr.Write(Event{
			Level:     LevelWarn,
			Subsystem: System,
			Name:      "logging.mongo.unavailable",
			Err:       err.Error(),
		})
		return New(stderr), noop
	}
	closeFn := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), logFlushTimeout)
		defer cancel()
		return ms.Close(ctx)
	}
	return New(NewMultiSink(stderr, ms)), closeFn
}
