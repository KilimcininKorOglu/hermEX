package rop

import (
	"errors"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// maybeReadReceipt sends the read receipt a message the client just marked read
// asks for, through the one receipt path every surface shares
// (mta.SendRequestedReceipt). The client decided: it asks for the receipt by
// leaving it unsuppressed, after its own setting or prompt. It is best-effort: a
// failure is logged and swallowed so it can never fail the SetMessageReadFlag that
// triggered it. A read-only session (no MTA bridge) sends nothing.
func (s *Session) maybeReadReceipt(store *objectstore.Store, messageID int64) {
	if s.accounts == nil {
		return
	}
	if err := mta.SendRequestedReceipt(s.accounts, s.spool, store, messageID, s.owner, mta.ReceiptClient, time.Now()); err != nil {
		s.logReceiptFailure(store, messageID, err)
	}
}

// logReceiptFailure records a read receipt that could not be produced. The failure
// is swallowed so it can never fail the read that triggered it, which is right and
// is also what makes it invisible: the sender is simply never told their message
// was read. The stage names which step failed, so a systematically broken send is
// distinguishable from a one-off store error.
func (s *Session) logReceiptFailure(store *objectstore.Store, messageID int64, err error) {
	stage := "unknown"
	if re, ok := errors.AsType[*mta.ReceiptError](err); ok {
		stage = re.Stage
	}
	s.logger.Emit(logging.Event{
		Level: logging.LevelError, Subsystem: logging.MAPI, Name: "readreceipt.fail",
		User: s.effectiveCaller(store),
		Fields: logging.Fields{
			"stage": stage, "mailbox": store.Dir(), "message": messageID,
		},
		Err: err.Error(),
	})
}
