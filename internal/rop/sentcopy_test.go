package rop

import (
	"errors"
	"testing"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// sentCopyFixture opens a session with a recording logger over a mailbox holding
// one draft, the state a submit is in once the message has gone out.
func sentCopyFixture(t *testing.T) (*Session, *ropSink, *objectstore.Store, int64) {
	t.Helper()
	sink := &ropSink{}
	sess := NewSession(provisionedDir(t), nil, "owner@hermex.test", WithLogger(logging.New(sink)))
	t.Cleanup(sess.Close)
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	st := sess.get(h[0]).store
	draft, err := st.CreateMessage(int64(mapi.PrivateFIDDraft), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "sent draft"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return sess, sink, st, draft
}

// TestAFailedSentCopyKeepsTheDraft proves that when the Sent Items copy of a
// message that has already gone out cannot be filed, the draft survives and the
// failure is recorded. Deleting the draft anyway left the sender with no copy of
// what was sent, and nothing recorded why.
func TestAFailedSentCopyKeepsTheDraft(t *testing.T) {
	sess, sink, st, draft := sentCopyFixture(t)
	orig := sentItemsAppend
	t.Cleanup(func() { sentItemsAppend = orig })
	sentItemsAppend = func(*objectstore.Store, []byte) error { return errors.New("disk full") }

	sess.fileSentCopy(st, draft, []byte("Subject: x\r\n\r\nbody\r\n"), true)

	if kept, err := st.OpenMessage(draft); err != nil || kept == nil {
		t.Fatalf("draft after a failed Sent copy: %v, want it kept", err)
	}
	e, ok := sink.find("submit.sentcopy.fail")
	if !ok {
		t.Fatal("no submit.sentcopy.fail event after a failed Sent copy")
	}
	if e.Err != "disk full" || e.Fields["draft"] != draft {
		t.Errorf("event = err %q draft %v, want disk full and %d", e.Err, e.Fields["draft"], draft)
	}
}

// TestAFiledSentCopyConsumesTheDraft proves the normal path: the copy lands in
// Sent Items, the draft is consumed, and no failure is recorded.
func TestAFiledSentCopyConsumesTheDraft(t *testing.T) {
	sess, sink, st, draft := sentCopyFixture(t)

	sess.fileSentCopy(st, draft, []byte("Subject: x\r\n\r\nbody\r\n"), true)

	if gone, err := st.OpenMessage(draft); !errors.Is(err, objectstore.ErrNotFound) || gone != nil {
		t.Fatalf("draft after a filed Sent copy: %v, want ErrNotFound", err)
	}
	sent, err := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Errorf("Sent Items holds %d messages, want 1", len(sent))
	}
	if n := sink.count("submit.sentcopy.fail") + sink.count("submit.draft.delete.fail"); n != 0 {
		t.Errorf("%d failure events on a clean submit, want 0", n)
	}
}

// TestNoOwnCopyFilesNothingInSentItems proves that a submit whose sender keeps no
// own copy files nothing in Sent Items and still consumes the draft.
func TestNoOwnCopyFilesNothingInSentItems(t *testing.T) {
	sess, sink, st, draft := sentCopyFixture(t)

	sess.fileSentCopy(st, draft, []byte("Subject: x\r\n\r\nbody\r\n"), false)

	sent, err := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Errorf("Sent Items holds %d messages without an own copy, want 0", len(sent))
	}
	if gone, err := st.OpenMessage(draft); !errors.Is(err, objectstore.ErrNotFound) || gone != nil {
		t.Errorf("draft without an own copy: %v, want ErrNotFound", err)
	}
	if n := sink.count("submit.sentcopy.fail") + sink.count("submit.draft.delete.fail"); n != 0 {
		t.Errorf("%d failure events, want 0", n)
	}
}

// TestAFailedDraftDeleteIsRecorded proves that a draft which cannot be consumed
// after the send is recorded, and that the other draft in the mailbox is left
// alone. A missing id stands in for a store that refuses the delete.
func TestAFailedDraftDeleteIsRecorded(t *testing.T) {
	sess, sink, st, draft := sentCopyFixture(t)
	missing := draft + 1000

	sess.fileSentCopy(st, missing, []byte("Subject: x\r\n\r\nbody\r\n"), false)

	e, ok := sink.find("submit.draft.delete.fail")
	if !ok {
		t.Fatal("no submit.draft.delete.fail event after a failed draft delete")
	}
	if e.Fields["draft"] != missing {
		t.Errorf("event draft = %v, want %d", e.Fields["draft"], missing)
	}
	if kept, err := st.OpenMessage(draft); err != nil || kept == nil {
		t.Errorf("unrelated draft after a failed delete: %v, want it kept", err)
	}
}
