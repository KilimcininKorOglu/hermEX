package logging_test

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"hermex/internal/logging"
)

// capture is an in-memory Sink for assertions.
type capture struct {
	mu     sync.Mutex
	events []logging.Event
}

func (c *capture) Write(e logging.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

// TestEmitStampsAndClassifies proves Emit fills the time and carries the level,
// subsystem, and name through unchanged.
func TestEmitStampsAndClassifies(t *testing.T) {
	c := &capture{}
	log := logging.New(c)
	log.Warn(logging.IMAP, "conn.idle.timeout", logging.Fields{"folder": "INBOX"})

	if len(c.events) != 1 {
		t.Fatalf("got %d events, want 1", len(c.events))
	}
	e := c.events[0]
	if e.Level != logging.LevelWarn || e.Subsystem != logging.IMAP || e.Name != "conn.idle.timeout" {
		t.Errorf("event = (%s, %s, %q), want (warn, imap, conn.idle.timeout)", e.Level, e.Subsystem, e.Name)
	}
	if e.Time.IsZero() {
		t.Error("Emit did not stamp the event time")
	}
	if e.Fields["folder"] != "INBOX" {
		t.Errorf("folder field = %v, want INBOX", e.Fields["folder"])
	}
}

// TestRedactionMasksSensitiveFields proves sensitive field keys are masked before
// storage and that the caller's own map is left untouched (redact must copy, not
// mutate, or a caller reusing the map would lose data).
func TestRedactionMasksSensitiveFields(t *testing.T) {
	c := &capture{}
	log := logging.New(c)

	in := logging.Fields{
		"user":          "alice@hermex.test",
		"password":      "hunter2",
		"Authorization": "Basic abc",
		"csrf_token":    "xyz",
		"folder":        "INBOX",
	}
	log.Info(logging.Auth, "login", in)

	e := c.events[0]
	for _, k := range []string{"password", "Authorization", "csrf_token"} {
		if e.Fields[k] != "[redacted]" {
			t.Errorf("field %q = %v, want [redacted]", k, e.Fields[k])
		}
	}
	if e.Fields["user"] != "alice@hermex.test" || e.Fields["folder"] != "INBOX" {
		t.Errorf("non-sensitive fields were altered: %v", e.Fields)
	}
	if in["password"] != "hunter2" {
		t.Error("redact mutated the caller's map instead of copying it")
	}
}

// TestNilLoggerIsNoOp proves a nil *Logger does nothing instead of panicking, so a
// not-yet-configured daemon can hold one.
func TestNilLoggerIsNoOp(t *testing.T) {
	var log *logging.Logger
	log.Info(logging.System, "startup", logging.Fields{"x": 1})
	log.Emit(logging.Event{Subsystem: logging.System, Name: "x"})
}

// TestStderrSinkRedactsOutput proves the full Logger -> StderrSink path renders the
// classifying columns and never leaks a sensitive value into the text line.
func TestStderrSinkRedactsOutput(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(logging.NewStderrSink(&buf))
	log.Error(logging.SMTP, "delivery.fail", logging.Fields{"to": "bob@hermex.test", "password": "sekret"})

	out := buf.String()
	for _, want := range []string{"error", "smtp", "delivery.fail", "to=bob@hermex.test", "[redacted]"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr line %q missing %q", out, want)
		}
	}
	if strings.Contains(out, "sekret") {
		t.Errorf("password leaked into the stderr line: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("stderr line not newline-terminated: %q", out)
	}
}

// TestLevelString pins the stored level names.
func TestLevelString(t *testing.T) {
	cases := map[logging.Level]string{
		logging.LevelDebug: "debug",
		logging.LevelInfo:  "info",
		logging.LevelWarn:  "warn",
		logging.LevelError: "error",
	}
	for lvl, want := range cases {
		if got := lvl.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", lvl, got, want)
		}
	}
}
