package nspi

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"hermex/internal/logging"
	"hermex/internal/mapi"
)

// eventSink records every event for assertion.
type eventSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (c *eventSink) Write(e logging.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *eventSink) named(name string) []logging.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []logging.Event
	for _, e := range c.events {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

func (c *eventSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// brokenStore makes a mailbox directory whose objects database is not a database,
// so opening it fails with an error other than objectstore.ErrNotProvisioned.
func brokenStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "objects.sqlite3"), []byte("this is not a database file at all"), 0o600); err != nil {
		t.Fatalf("write the broken database: %v", err)
	}
	return dir
}

// wantServedWithoutValues asserts the row was answered and carries the
// PT_ERROR(ecNotFound) marker, not a value, for each unreadable column.
func wantServedWithoutValues(t *testing.T, what string, result uint32, row mapi.PropertyValues, cols []mapi.PropTag) {
	t.Helper()
	if result == ecError {
		t.Fatalf("%s: result = %#x, the row was refused instead of served", what, result)
	}
	for _, tag := range cols {
		if v, ok := row.Get(errorTag(tag)); !ok || v != ecNotFound {
			t.Errorf("%s: %#x marker = %v (present %v), want ecNotFound", what, uint32(tag), v, ok)
		}
	}
}

// TestGALRowRecordsAnUnreadableMailbox proves a portrait or certificate read that
// fails is recorded while the row is still answered, because a failure swallowed
// to keep a path serving must reach the operator's log; a mailbox that was never
// provisioned has neither and records nothing.
func TestGALRowRecordsAnUnreadableMailbox(t *testing.T) {
	sink := &eventSink{}
	s := NewServer(maskedGAL{
		{DisplayName: "alice@hermex.test", Address: "alice@hermex.test", StorePath: brokenStore(t)},
		{DisplayName: "bob@hermex.test", Address: "bob@hermex.test", StorePath: filepath.Join(t.TempDir(), "never")},
	}, testGUID)
	s.Logger = logging.New(sink)
	cols := []mapi.PropTag{mapi.PrEmsAbThumbnailPhoto, mapi.PrEmsAbX509Cert}

	result, row := decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midBase, codePage: 1252}, cols), testCaller))
	wantServedWithoutValues(t, "unreadable mailbox", result, row, cols)
	for _, name := range []string{"gal.photo.read.fail", "gal.cert.read.fail"} {
		evs := sink.named(name)
		if len(evs) != 1 {
			t.Fatalf("%s events = %d, want 1", name, len(evs))
		}
		if evs[0].Fields["address"] != "alice@hermex.test" || evs[0].Err == "" {
			t.Errorf("%s = %+v, want the address and the error", name, evs[0])
		}
	}

	before := sink.count()
	result, row = decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midBase + 1, codePage: 1252}, cols), testCaller))
	wantServedWithoutValues(t, "never-provisioned mailbox", result, row, cols)
	if got := sink.count(); got != before {
		t.Errorf("a never-provisioned mailbox recorded %d events, want none", got-before)
	}
}
