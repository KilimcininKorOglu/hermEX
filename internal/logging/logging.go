// Package logging is hermEX's central, structured activity log. Every subsystem
// emits Events — a connection, an authentication attempt, a protocol operation,
// an error — through a Logger to a Sink. The schema is original and independent
// of the mailbox store's on-disk schema: one flat Event document with a small set
// of indexed columns (time, level, subsystem, user) plus a free-form Fields map
// for the rest.
//
// This file is the schema and the synchronous core: the Event document, the
// subsystem taxonomy, the Sink interface, a stderr sink, and a Logger that stamps
// the time and redacts obviously-sensitive fields before writing. The
// asynchronous MongoDB sink (with a bounded buffer, local-disk spill, and replay)
// is a separate sink that satisfies the same interface.
package logging

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// Level is an Event's severity.
type Level uint8

// The severity levels, in increasing order.
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// String returns the lowercase level name used in the stored document.
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}

// Subsystem identifies the part of the system an Event came from. The set is the
// coverage checklist for "log everything": every transport, the cross-cutting
// security/sync layers, and the operational surfaces.
type Subsystem string

// The subsystem taxonomy. Keep this list and the instrumentation in lockstep —
// each value should have at least one emitting call site and an integration test
// that asserts a document lands.
const (
	IMAP       Subsystem = "imap"
	POP3       Subsystem = "pop3"
	SMTP       Subsystem = "smtp"
	MTA        Subsystem = "mta"
	EWS        Subsystem = "ews"
	MAPI       Subsystem = "mapi"
	RPC        Subsystem = "rpc"
	DAV        Subsystem = "dav"
	ActiveSync Subsystem = "activesync"
	NSPI       Subsystem = "nspi"
	ROP        Subsystem = "rop"
	ICS        Subsystem = "ics"
	SMIME      Subsystem = "smime"
	TLS        Subsystem = "tls"
	Webmail    Subsystem = "webmail"
	Gateway    Subsystem = "gateway"
	Notify     Subsystem = "notify"
	Admin      Subsystem = "admin"
	Store      Subsystem = "store"
	Directory  Subsystem = "directory"
	Auth       Subsystem = "auth"
	System     Subsystem = "system"
)

// Fields is the free-form, structured part of an Event. Never put credentials,
// tokens, or message bodies here; the Logger scrubs obviously-sensitive keys
// defensively, but the call site is the real guard.
type Fields map[string]any

// Event is one log document. Time, Level, Subsystem, and User are the indexed
// columns the admin panel filters on; the rest carry detail. Empty fields are
// omitted from the stored document.
type Event struct {
	Time       time.Time
	Level      Level
	Subsystem  Subsystem
	Name       string // dotted event name, e.g. "auth.fail", "conn.accept"
	User       string
	RemoteAddr string
	RequestID  string
	DurationMs int64
	Fields     Fields
	Err        string
}

// Sink consumes finished Events. Implementations must be safe for concurrent use
// and must not block the caller for long — the protocol hot paths emit events, so
// a slow sink (a remote database) buffers asynchronously rather than blocking.
type Sink interface {
	Write(Event)
}

// Logger stamps and redacts Events, then hands them to its Sink. A nil *Logger is
// a no-op, so a not-yet-configured daemon can hold one without nil checks at every
// call site.
type Logger struct {
	sink Sink
}

// New returns a Logger writing to sink.
func New(sink Sink) *Logger { return &Logger{sink: sink} }

// Emit stamps the event time (if unset), redacts sensitive fields, and writes the
// event. It is the primitive the convenience methods build on; callers that need
// the User/RemoteAddr/RequestID columns build the Event and Emit it directly.
func (l *Logger) Emit(e Event) {
	if l == nil || l.sink == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	e.Fields = redact(e.Fields)
	l.sink.Write(e)
}

// Debug, Info, Warn, and Error emit an event at the named level for sub.
func (l *Logger) Debug(sub Subsystem, name string, f Fields) {
	l.Emit(Event{Level: LevelDebug, Subsystem: sub, Name: name, Fields: f})
}

func (l *Logger) Info(sub Subsystem, name string, f Fields) {
	l.Emit(Event{Level: LevelInfo, Subsystem: sub, Name: name, Fields: f})
}

func (l *Logger) Warn(sub Subsystem, name string, f Fields) {
	l.Emit(Event{Level: LevelWarn, Subsystem: sub, Name: name, Fields: f})
}

func (l *Logger) Error(sub Subsystem, name string, f Fields) {
	l.Emit(Event{Level: LevelError, Subsystem: sub, Name: name, Fields: f})
}

// sensitiveKey reports whether a field key names something that must never be
// stored verbatim. Matching is case-insensitive and substring-based so
// "Authorization", "user_password", and "csrf_token" are all caught.
func sensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, bad := range []string{"password", "passwd", "secret", "token", "authorization", "cookie", "credential"} {
		if strings.Contains(k, bad) {
			return true
		}
	}
	return false
}

// redact returns a copy of f with sensitive values masked. It returns f's nil/empty
// state unchanged so an event without fields stays field-less.
func redact(f Fields) Fields {
	if len(f) == 0 {
		return f
	}
	out := make(Fields, len(f))
	for k, v := range f {
		if sensitiveKey(k) {
			out[k] = "[redacted]"
		} else {
			out[k] = v
		}
	}
	return out
}

// StderrSink writes a one-line, human-readable rendering of each event to a
// writer (os.Stderr by default). It is the always-on operator-visibility sink the
// MongoDB sink augments rather than replaces, and it keeps the dev/Docker logs
// working. It is safe for concurrent use: each Write is a single io.Writer call.
type StderrSink struct {
	w io.Writer
}

// NewStderrSink returns a StderrSink writing to w, or os.Stderr when w is nil.
func NewStderrSink(w io.Writer) *StderrSink {
	if w == nil {
		w = os.Stderr
	}
	return &StderrSink{w: w}
}

// Write renders e as a single line: time, level, subsystem, name, then the
// non-empty columns and sorted fields.
func (s *StderrSink) Write(e Event) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %-5s %s %s", e.Time.Format(time.RFC3339), e.Level, e.Subsystem, e.Name)
	if e.User != "" {
		fmt.Fprintf(&b, " user=%s", e.User)
	}
	if e.RemoteAddr != "" {
		fmt.Fprintf(&b, " remote=%s", e.RemoteAddr)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " req=%s", e.RequestID)
	}
	if e.DurationMs != 0 {
		fmt.Fprintf(&b, " dur=%dms", e.DurationMs)
	}
	for _, k := range sortedKeys(e.Fields) {
		fmt.Fprintf(&b, " %s=%v", k, e.Fields[k])
	}
	if e.Err != "" {
		fmt.Fprintf(&b, " err=%q", e.Err)
	}
	b.WriteByte('\n')
	io.WriteString(s.w, b.String())
}

// sortedKeys returns f's keys in sorted order, for a stable one-line rendering.
func sortedKeys(f Fields) []string {
	if len(f) == 0 {
		return nil
	}
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
