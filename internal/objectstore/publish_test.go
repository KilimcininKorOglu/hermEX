package objectstore

import (
	"sync"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// capturedEvents collects the change events a store publishes, filtered to one
// mailbox so a parallel test on another temp store cannot pollute the capture.
type capturedEvents struct {
	mu  sync.Mutex
	dir string
	ev  []ChangeEvent
}

func (c *capturedEvents) publish(e ChangeEvent) {
	if e.MailboxDir != c.dir {
		return
	}
	c.mu.Lock()
	c.ev = append(c.ev, e)
	c.mu.Unlock()
}

// drain returns the events captured since the last drain and clears the buffer.
func (c *capturedEvents) drain() []ChangeEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.ev
	c.ev = nil
	return out
}

// hasOp reports whether any captured event used op.
func hasOp(events []ChangeEvent, op string) bool {
	for _, e := range events {
		if e.Op == op {
			return true
		}
	}
	return false
}

// publishTestMessage is a minimal well-formed RFC822 message the import path
// accepts, used to drive the delivery instrumentation.
func publishTestMessage() []byte {
	return []byte("From: a@example.test\r\n" +
		"To: b@example.test\r\n" +
		"Subject: notify probe\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n" +
		"\r\n" +
		"body\r\n")
}

// TestChangePublisher proves each instrumented mutation publishes a change event
// keyed to the mutated mailbox, with the right op — the contract the push
// consumers wake on. The cn/op/mid are enrichment; the load-bearing assertion is
// that every content-changing write fires exactly one consumer-visible wake of the
// expected kind, so a missed site (which would silently strand a consumer on its
// poll cadence) fails here.
func TestChangePublisher(t *testing.T) {
	s := openSeededStore(t)
	cap := &capturedEvents{dir: s.Dir()}
	SetChangePublisher(cap.publish)
	t.Cleanup(func() { SetChangePublisher(nil) })

	inbox := int64(mapi.PrivateFIDInbox)

	// Delivery: AppendMessage publishes a create for the object store, then a second
	// create once the IMAP index row exists — both for this mailbox.
	info, err := s.AppendMessage(inbox, publishTestMessage(), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := cap.drain(); !hasOp(got, "create") {
		t.Errorf("AppendMessage published %+v, want a create", got)
	}

	// Flag change: SetMessageFlags writes the index and mirrors read state.
	if err := s.SetMessageFlags(inbox, info.UID, FlagSeen); err != nil {
		t.Fatal(err)
	}
	if got := cap.drain(); !hasOp(got, "flags") {
		t.Errorf("SetMessageFlags published %+v, want a flags", got)
	}

	// Edit: ModifyMessageProperties bumps the change number.
	if err := s.ModifyMessageProperties(info.ID, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "edited"}}); err != nil {
		t.Fatal(err)
	}
	if got := cap.drain(); !hasOp(got, "modify") {
		t.Errorf("ModifyMessageProperties published %+v, want a modify", got)
	}

	// Folder create.
	if _, err := s.CreateFolder(nil, "Notify Probe"); err != nil {
		t.Fatal(err)
	}
	if got := cap.drain(); !hasOp(got, "folder") {
		t.Errorf("CreateFolder published %+v, want a folder", got)
	}

	// Hard delete: the event carries the message's mid since the delete bumps no
	// change number.
	mid := midString(uint64(info.ID))
	if err := s.DeleteMessage(inbox, info.UID); err != nil {
		t.Fatal(err)
	}
	del := cap.drain()
	if !hasOp(del, "delete") {
		t.Fatalf("DeleteMessage published %+v, want a delete", del)
	}
	carriedMid := false
	for _, e := range del {
		if e.Op == "delete" && e.Mid == mid {
			carriedMid = true
		}
	}
	if !carriedMid {
		t.Errorf("delete event did not carry mid %q: %+v", mid, del)
	}
}

// TestChangePublisherSoftDelete proves the soft-delete path (Recoverable Items)
// publishes a delete, since the message leaves every live view.
func TestChangePublisherSoftDelete(t *testing.T) {
	s := openSeededStore(t)
	cap := &capturedEvents{dir: s.Dir()}
	SetChangePublisher(cap.publish)
	t.Cleanup(func() { SetChangePublisher(nil) })

	inbox := int64(mapi.PrivateFIDInbox)
	info, err := s.AppendMessage(inbox, publishTestMessage(), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	cap.drain()

	if err := s.SoftDeleteMessage(inbox, info.UID); err != nil {
		t.Fatal(err)
	}
	if got := cap.drain(); !hasOp(got, "delete") {
		t.Errorf("SoftDeleteMessage published %+v, want a delete", got)
	}
}

// TestChangePublisherNilIsNoop proves a store with no publisher installed runs its
// mutations unchanged — the degradation floor that keeps the mail path independent
// of the push relay.
func TestChangePublisherNilIsNoop(t *testing.T) {
	SetChangePublisher(nil)
	s := openSeededStore(t)
	if _, err := s.AppendMessage(int64(mapi.PrivateFIDInbox), publishTestMessage(), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("AppendMessage with nil publisher: %v", err)
	}
}
