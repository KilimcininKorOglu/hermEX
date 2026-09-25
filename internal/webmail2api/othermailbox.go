package webmail2api

import (
	"errors"

	"hermex/internal/logging"
	"hermex/internal/objectstore"
)

// openOtherMailbox opens a mailbox that is not the caller's own session mailbox,
// such as a free/busy target, a recall recipient or a certificate owner, only
// when it already exists. Reading another user's mailbox must never provision
// one: a user who has not yet logged in or received mail has no store, and
// opening it with objectstore.Open would create an empty one as a side effect of
// someone else's request. ok is false when there is no store or it cannot be
// opened; an open failure other than a missing store is recorded under op.
func openOtherMailbox(path, op, address string) (*objectstore.Store, bool) {
	st, err := objectstore.OpenExisting(path)
	if errors.Is(err, objectstore.ErrNotProvisioned) {
		return nil, false
	}
	if err != nil {
		logError(op, err, logging.Fields{"mailbox": address})
		return nil, false
	}
	return st, true
}
