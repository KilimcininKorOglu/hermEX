package spooler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// outboxMessage returns the seeded Outbox message's index row, the shape the
// production sweep hands to the give-up paths.
func outboxMessage(t *testing.T, st *objectstore.Store) objectstore.MessageInfo {
	t.Helper()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDOutbox))
	mustNoErr(t, err, "list the Outbox")
	if len(msgs) != 1 {
		t.Fatalf("the Outbox holds %d messages, want the seeded one", len(msgs))
	}
	return msgs[0]
}

// TestAnUnreadableGiveUpReportIsReported is the load-bearing case. The two reads
// that build the report were made with their errors discarded, so a message the
// store could not read left the Outbox with no report and no trace: the sender was
// never told the scheduled send was abandoned. The failure now rides out on the
// returned error, which the sweep joins and the operator sees.
func TestAnUnreadableGiveUpReportIsReported(t *testing.T) {
	st := openStore(t)
	outbox := int64(mapi.PrivateFIDOutbox)

	var gaveUp int
	onGiveUp := func(raw []byte, recipients []string, cause error) { gaveUp++ }

	// A row naming a message this store does not hold: both reads fail, which is
	// what a damaged or concurrently removed message looks like from here.
	err := returnAmbiguous(st, onGiveUp, outbox, objectstore.MessageInfo{ID: 999999, UID: 999999})
	if err == nil {
		t.Fatal("an unreadable message was abandoned without reporting anything")
	}
	wantContains(t, err.Error(), "give-up report: read the message", "the object read failure")
	wantContains(t, err.Error(), "give-up report: read the wire copy", "the wire-copy read failure")
	wantEq(t, gaveUp, 0, "give-up hook calls with no wire copy to report")
}

// TestAReadableGiveUpReportStillReachesTheSender keeps the reporting path intact:
// the fix must not turn a message the store CAN read into a silent one.
func TestAReadableGiveUpReportStillReachesTheSender(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))
	m := outboxMessage(t, st)

	var gotRaw []byte
	var gotRecipients []string
	var gaveUp int
	onGiveUp := func(raw []byte, recipients []string, cause error) {
		gaveUp++
		gotRaw, gotRecipients = raw, recipients
	}

	err := returnAmbiguous(st, onGiveUp, int64(mapi.PrivateFIDOutbox), m)
	if !errors.Is(err, ErrAmbiguousRelease) {
		t.Fatalf("error = %v, want the ambiguous-release cause", err)
	}
	if strings.Contains(err.Error(), "give-up report") {
		t.Errorf("a readable message reported a read failure: %v", err)
	}
	wantEq(t, gaveUp, 1, "give-up hook calls")
	wantContains(t, string(gotRaw), "Subject: scheduled", "the report carries the abandoned message")
	if len(gotRecipients) == 0 {
		t.Error("the report names no recipient although the message could be read")
	}
	wantEq(t, count(t, st, int64(mapi.PrivateFIDOutbox)), 0, "Outbox messages after the return")
}
