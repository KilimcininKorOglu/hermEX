package spooler

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// openStore provisions a fresh, fully seeded mailbox (Outbox and Sent present).
func openStore(t *testing.T) *objectstore.Store {
	t.Helper()
	st, err := objectstore.Open(filepath.Join(t.TempDir(), "alice"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// scheduleOutbox files a To/Cc/Bcc message in the Outbox and stamps it with a
// deferred-send time, the shape a send-later compose produces.
func scheduleOutbox(t *testing.T, st *objectstore.Store, when time.Time) {
	t.Helper()
	raw := "From: alice@hermex.test\r\n" +
		"To: to@example.com\r\n" +
		"Cc: cc@example.com\r\n" +
		"Bcc: bcc@example.com\r\n" +
		"Subject: scheduled\r\n" +
		"\r\n" +
		"scheduled body\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDOutbox), []byte(raw), time.Unix(1, 0), objectstore.FlagSeen)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrDeferredSendTime, Value: mapi.UnixToNTTime(when)},
	}); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, st *objectstore.Store, fid int64) int {
	t.Helper()
	msgs, err := st.ListMessages(fid)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

// TestProcessDueOutboxReleasesDueMessage checks the core release path: a past-due
// scheduled message is delivered to every recipient (To, Cc, and the blind Bcc),
// the delivered wire copy has the Bcc header stripped (the blind list must never
// reach the wire) while the Sent copy keeps it, and the Outbox is cleared.
func TestProcessDueOutboxReleasesDueMessage(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))

	var gotRcpts []string
	var gotRaw []byte
	deliver := func(rcpts []string, raw []byte, when time.Time) ([]string, error) {
		gotRcpts = slices.Clone(rcpts)
		gotRaw = slices.Clone(raw)
		return nil, nil
	}

	released, err := ProcessDueOutbox(st, deliver, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("released %d, want 1", released)
	}

	// Every recipient — including the blind Bcc — must be delivered to.
	for _, want := range []string{"to@example.com", "cc@example.com", "bcc@example.com"} {
		if !slices.Contains(gotRcpts, want) {
			t.Errorf("delivery recipients %v missing %q", gotRcpts, want)
		}
	}
	// The delivered bytes carry To and Cc but never the blind Bcc address (which
	// appears only in the Bcc header, so its absence proves the header was cut).
	dw := string(gotRaw)
	if !strings.Contains(dw, "to@example.com") || !strings.Contains(dw, "cc@example.com") {
		t.Errorf("delivered copy lost To/Cc:\n%s", dw)
	}
	if strings.Contains(dw, "bcc@example.com") {
		t.Errorf("delivered copy leaked the blind Bcc:\n%s", dw)
	}

	// The Outbox is cleared and the Sent copy keeps the Bcc record.
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 0 {
		t.Errorf("Outbox has %d after release, want 0", n)
	}
	sent, err := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("Sent has %d, want 1", len(sent))
	}
	sentRaw, err := st.GetMessageRaw(int64(mapi.PrivateFIDSentItems), sent[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sentRaw), "bcc@example.com") {
		t.Errorf("Sent copy should keep the Bcc record:\n%s", sentRaw)
	}
}

// TestProcessDueOutboxSkipsFutureMessage checks that a message whose deferred
// time has not yet come is left untouched and not delivered.
func TestProcessDueOutboxSkipsFutureMessage(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(time.Hour))

	called := false
	deliver := func(rcpts []string, raw []byte, when time.Time) ([]string, error) {
		called = true
		return nil, nil
	}
	released, err := ProcessDueOutbox(st, deliver, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if released != 0 || called {
		t.Errorf("a future message was released (released=%d, deliver called=%v)", released, called)
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 1 {
		t.Errorf("future message left the Outbox (count=%d, want 1)", n)
	}
}

// TestProcessDueOutboxKeepsOnDeliverError checks that a delivery failure leaves
// the message in the Outbox to retry and files nothing to Sent, and that the
// failure is reported.
func TestProcessDueOutboxKeepsOnDeliverError(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))

	deliver := func(rcpts []string, raw []byte, when time.Time) ([]string, error) {
		return nil, errors.New("transport unavailable")
	}
	released, err := ProcessDueOutbox(st, deliver, time.Now())
	if released != 0 {
		t.Errorf("released %d on delivery failure, want 0", released)
	}
	if err == nil {
		t.Error("a delivery failure should be reported")
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 1 {
		t.Errorf("a failed message must stay in the Outbox (count=%d, want 1)", n)
	}
	if n := count(t, st, int64(mapi.PrivateFIDSentItems)); n != 0 {
		t.Errorf("a failed message must not be filed to Sent (count=%d, want 0)", n)
	}
}
