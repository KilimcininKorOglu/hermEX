package directory

import (
	"context"
	"testing"
	"time"
)

// TestTryLockExcludesASecondHolder proves the advisory lock does what the
// background loops need: while one instance holds it, another instance asking for
// the same lock is told no instead of running the same pass concurrently.
func TestTryLockExcludesASecondHolder(t *testing.T) {
	ctx := context.Background()
	first := NewSQL(openTestDB(t))
	second := NewSQL(openTestDB(t)) // a separate pool, standing in for another daemon

	release, ok, err := first.TryLock(ctx, LockSendLater)
	if err != nil || !ok {
		t.Fatalf("first TryLock = ok %v err %v, want the lock", ok, err)
	}

	if _, ok, err := second.TryLock(ctx, LockSendLater); err != nil || ok {
		t.Fatalf("second TryLock = ok %v err %v, want refused while the first holds it", ok, err)
	}

	release()

	release2, ok, err := second.TryLock(ctx, LockSendLater)
	if err != nil || !ok {
		t.Fatalf("TryLock after release = ok %v err %v, want the lock", ok, err)
	}
	release2()
}

// TestTryLockDoesNotWait proves a refused caller returns at once rather than
// queueing: a loop that queued would pile up passes behind the holder instead of
// skipping its tick.
func TestTryLockDoesNotWait(t *testing.T) {
	ctx := context.Background()
	first := NewSQL(openTestDB(t))
	second := NewSQL(openTestDB(t))

	release, ok, err := first.TryLock(ctx, LockRelayDrain)
	if err != nil || !ok {
		t.Fatalf("first TryLock = ok %v err %v", ok, err)
	}
	defer release()

	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		rel, ok, err := second.TryLock(ctx, LockRelayDrain)
		if ok {
			rel()
		}
		done <- result{ok, err}
	}()
	select {
	case r := <-done:
		mustNoErr(t, "second TryLock", r.err)
		wantEq(t, "the second caller got the held lock", r.ok, false)
	case <-time.After(3 * time.Second):
		t.Fatal("TryLock blocked instead of reporting the lock as held")
	}
}

// TestTryLockExcludesTheSamePool proves two loops of one daemon, sharing one
// connection pool, also exclude each other: the lock is held by the pinned
// connection, so another connection of the pool is refused.
func TestTryLockExcludesTheSamePool(t *testing.T) {
	ctx := context.Background()
	d := NewSQL(openTestDB(t))
	release, ok, err := d.TryLock(ctx, LockSendLater)
	if err != nil || !ok {
		t.Fatalf("first TryLock = ok %v err %v", ok, err)
	}
	defer release()
	_, ok, err = d.TryLock(ctx, LockSendLater)
	mustNoErr(t, "second TryLock on the same pool", err)
	wantEq(t, "the same pool got the held lock twice", ok, false)
}

// TestTryLockNamesAreIndependent proves the two loops do not exclude each other:
// the send-later sweep and the relay drain run concurrently in one process.
func TestTryLockNamesAreIndependent(t *testing.T) {
	ctx := context.Background()
	d := NewSQL(openTestDB(t))

	releaseA, ok, err := d.TryLock(ctx, LockSendLater)
	if err != nil || !ok {
		t.Fatalf("send-later TryLock = ok %v err %v", ok, err)
	}
	defer releaseA()

	releaseB, ok, err := d.TryLock(ctx, LockRelayDrain)
	if err != nil || !ok {
		t.Fatalf("relay TryLock = ok %v err %v, want it unaffected by the other lock", ok, err)
	}
	releaseB()
}

// TestReleaseIsIdempotent proves a deferred release that also ran explicitly does
// not double-close the pinned connection.
func TestReleaseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d := NewSQL(openTestDB(t))
	release, ok, err := d.TryLock(ctx, LockSendLater)
	if err != nil || !ok {
		t.Fatalf("TryLock = ok %v err %v", ok, err)
	}
	release()
	release()

	release2, ok, err := d.TryLock(ctx, LockSendLater)
	if err != nil || !ok {
		t.Fatalf("TryLock after a double release = ok %v err %v, want the lock free", ok, err)
	}
	release2()
}
