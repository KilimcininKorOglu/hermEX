package directory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Named advisory locks that serialize the background loops which must run in
// exactly one process at a time. Each names one loop; a daemon takes the lock for
// the duration of a single pass, so nothing depends on a particular instance
// being "the" leader.
const (
	// LockSendLater guards the scheduled-send sweep. Two sweepers could deliver
	// the same Outbox message twice, in the window between its delivery and its
	// removal from the Outbox.
	LockSendLater = "hermex_sendlater_sweep"
	// LockRelayDrain guards the outbound relay drain. The spool's Claim does not
	// lease the rows it returns, so two drainers would deliver the same recipient
	// twice.
	LockRelayDrain = "hermex_relay_drain"
	// LockDigest guards the quarantine digest pass. Each mailbox's watermark is
	// advanced only after its digest is delivered, so two concurrent passes read
	// the same stale watermark and each sends a full digest, with its own valid
	// release links. The watermark alone dedups passes that do not overlap.
	LockDigest = "hermex_quarantine_digest"
	// LockWebmailSessionPrune and LockAdminSessionPrune guard the two expired-session
	// sweeps. The delete itself is idempotent, so a second pass is harmless; the locks
	// keep several instances from running the same scan against one database every
	// minute. They are separate names because each daemon prunes only its own table,
	// and one shared name would let whichever instance won leave the other table
	// untouched.
	LockWebmailSessionPrune = "hermex_webmail_session_prune"
	LockAdminSessionPrune   = "hermex_admin_session_prune"
)

// TryLock takes the named advisory lock without waiting and returns a function
// that releases it. ok is false when another instance already holds the lock, in
// which case the caller skips this pass; err reports a database failure, which the
// caller must also treat as "do not run" rather than proceeding unguarded.
//
// The lock is held on one pinned connection, because GET_LOCK is connection
// scoped. That is also what makes it self-healing: an instance that dies mid-pass
// drops its connection, the server releases the lock, and the next instance's next
// tick takes over. Releasing is idempotent and safe to defer.
func (d *SQLDirectory) TryLock(ctx context.Context, name string) (release func(), ok bool, err error) {
	c, err := d.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var got sql.NullInt64
	// A zero timeout means "report failure immediately" rather than queueing every
	// instance behind the holder, which would pile up passes instead of skipping.
	if err := c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&got); err != nil {
		return nil, false, errors.Join(err, c.Close())
	}
	if !got.Valid {
		return nil, false, errors.Join(fmt.Errorf("directory: advisory lock %q errored", name), c.Close())
	}
	if got.Int64 != 1 {
		return nil, false, c.Close() // held elsewhere; nil unless the close itself failed
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		// Release explicitly so the lock frees the moment the pass ends, then close.
		// A failing RELEASE_LOCK needs no handling: closing the connection releases
		// the lock too, which is the guarantee this relies on. Use a cancel-free
		// context so a shutdown mid-pass still runs the release.
		_, _ = c.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", name)
		_ = c.Close() // best-effort teardown; the closed connection releases the lock regardless
	}, true, nil
}
