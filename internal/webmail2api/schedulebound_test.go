package webmail2api

import (
	"testing"
	"time"

	"hermex/internal/mapi"
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
	defer mustClose(t, st)

	raw := []byte("From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: x\r\n\r\nbody\r\n")

	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	if err := scheduleOutbox(st, raw, past); err == nil {
		t.Error("past send time was accepted, want rejection")
	}

	tooFar := time.Now().Add(maxScheduleAhead + 48*time.Hour).Format(time.RFC3339)
	if err := scheduleOutbox(st, raw, tooFar); err == nil {
		t.Error("far-future send time was accepted, want rejection")
	}

	for _, bad := range []string{"", "tomorrow", "2026-13-40T99:00:00Z"} {
		if err := scheduleOutbox(st, raw, bad); err == nil {
			t.Errorf("send time %q was accepted, want rejection", bad)
		}
	}
	outbox := func() int {
		msgs, err := st.ListMessages(int64(mapi.PrivateFIDOutbox))
		mustNoErr(t, "list outbox", err)
		return len(msgs)
	}
	wantEq(t, "outbox after the refused schedules", outbox(), 0)

	ok := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	if err := scheduleOutbox(st, raw, ok); err != nil {
		t.Errorf("valid send time rejected: %v", err)
	}
	wantEq(t, "outbox after a valid schedule", outbox(), 1)
}
