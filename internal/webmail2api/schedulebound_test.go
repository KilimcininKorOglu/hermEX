package webmail2api

import (
	"testing"
	"time"

	"hermex/internal/objectstore"
)

// TestScheduleOutboxBounds pins the send-later window: a past time and a
// far-future time (beyond one year) are rejected, while a time inside the window
// is accepted and filed. This keeps a scheduled send from becoming a permanent
// dead letter and from firing immediately as if not scheduled.
func TestScheduleOutboxBounds(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	raw := []byte("From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: x\r\n\r\nbody\r\n")

	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	if err := scheduleOutbox(st, raw, past); err == nil {
		t.Error("past send time was accepted, want rejection")
	}

	tooFar := time.Now().Add(maxScheduleAhead + 48*time.Hour).Format(time.RFC3339)
	if err := scheduleOutbox(st, raw, tooFar); err == nil {
		t.Error("far-future send time was accepted, want rejection")
	}

	ok := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	if err := scheduleOutbox(st, raw, ok); err != nil {
		t.Errorf("valid send time rejected: %v", err)
	}
}
