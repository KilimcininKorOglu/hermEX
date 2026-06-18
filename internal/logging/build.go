package logging

import (
	"context"
	"path/filepath"
	"time"
)

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
// and a close function (flush + disconnect) to run as a shutdown cleanup.
//
// database is the Mongo database holding the logs collection; spillDir, when set,
// is where failed batches are buffered while Mongo is unreachable; retention is
// the TTL window.
func Build(mongoURI, database, spillDir string, retention time.Duration) (*Logger, func(context.Context) error) {
	stderr := NewStderrSink(nil)
	noop := func(context.Context) error { return nil }
	if mongoURI == "" {
		return New(stderr), noop
	}

	spillPath := ""
	if spillDir != "" {
		spillPath = filepath.Join(spillDir, "logspill.jsonl")
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
	return New(NewMultiSink(stderr, ms)), ms.Close
}
