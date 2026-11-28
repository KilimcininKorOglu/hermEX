package rop

import (
	"errors"
	"testing"

	"hermex/internal/logging"
	"hermex/internal/objectstore"
)

// closeFixture opens a session with a recording logger and one logon handle.
func closeFixture(t *testing.T) (*Session, *ropSink, uint32) {
	t.Helper()
	sink := &ropSink{}
	sess := NewSession(provisionedDir(t), nil, "owner@hermex.test", WithLogger(logging.New(sink)))
	t.Cleanup(sess.Close)
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	return sess, sink, h[0]
}

// TestAFailedStoreCloseIsRecorded proves that a store close failing on release
// is recorded with the mailbox it belongs to, and that the handle is still freed:
// the failure must not leave a half-released logon behind.
func TestAFailedStoreCloseIsRecorded(t *testing.T) {
	sess, sink, h := closeFixture(t)
	dir := sess.get(h).store.Dir()
	orig := storeClose
	t.Cleanup(func() { storeClose = orig })
	storeClose = func(st *objectstore.Store) error {
		if err := orig(st); err != nil {
			t.Errorf("real close: %v", err)
		}
		return errors.New("fsync failed")
	}

	sess.release(h)

	e, ok := sink.find("store.close.fail")
	if !ok {
		t.Fatal("no store.close.fail event after a failed store close")
	}
	if e.Err != "fsync failed" || e.Fields["mailbox"] != dir {
		t.Errorf("event = err %q mailbox %v, want fsync failed and %s", e.Err, e.Fields["mailbox"], dir)
	}
	if sess.get(h) != nil {
		t.Error("handle still held after a failed store close, want it freed")
	}
}

// TestACleanStoreCloseRecordsNothing proves the normal release path frees the
// handle and records no failure.
func TestACleanStoreCloseRecordsNothing(t *testing.T) {
	sess, sink, h := closeFixture(t)

	sess.release(h)

	if n := sink.count("store.close.fail"); n != 0 {
		t.Errorf("%d store.close.fail events on a clean close, want 0", n)
	}
	if sess.get(h) != nil {
		t.Error("handle still held after release, want it freed")
	}
}
