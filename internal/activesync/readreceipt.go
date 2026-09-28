package activesync

import (
	"errors"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// sendReadReceipts sends the read receipts owed for the messages a device marked
// read, as Exchange does for a read on an ActiveSync device, unless the mailbox
// turned that off (objectstore.ReadReceiptConfig.SuppressActiveSync). It goes
// through the receipt path every surface shares. A failure is logged and never
// fails the Sync that triggered it.
func (s *Server) sendReadReceipts(sess *session, st *objectstore.Store, becameRead []int64) {
	if len(becameRead) == 0 || s.accounts == nil {
		return
	}
	cfg, err := st.GetReadReceiptConfig()
	if err != nil {
		// An unreadable setting sends nothing: sending could override a reader's
		// choice not to, while the request stays for a later read to honour.
		s.logReceiptFailure(sess, 0, "config", err)
		return
	}
	if cfg.SuppressActiveSync {
		return
	}
	for _, id := range becameRead {
		err := mta.SendRequestedReceipt(s.accounts, s.Spool, st, id, sess.user, false, time.Now())
		if err == nil {
			continue
		}
		stage := "unknown"
		if re, ok := errors.AsType[*mta.ReceiptError](err); ok {
			stage = re.Stage
		}
		s.logReceiptFailure(sess, id, stage, err)
	}
}

// logReceiptFailure records a read receipt that could not be produced, naming the
// step that failed.
func (s *Server) logReceiptFailure(sess *session, messageID int64, stage string, err error) {
	s.Logger.Emit(logging.Event{
		Level:      logging.LevelError,
		Subsystem:  logging.ActiveSync,
		Name:       "readreceipt.fail",
		User:       sess.user,
		RemoteAddr: sess.tel.IP,
		Fields:     logging.Fields{"stage": stage, "message": messageID, "device": sess.req.deviceID},
		Err:        err.Error(),
	})
}
