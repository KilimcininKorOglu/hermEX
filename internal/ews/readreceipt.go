package ews

import (
	"errors"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// Read receipts over EWS follow Exchange: marking a message read, through
// UpdateItem's IsRead or MarkAllItemsAsRead, sends the receipt the message asks
// for unless the request sets SuppressReadReceipts, and a SuppressReadReceipt
// response object declines it without marking anything.

// sendReadReceipt sends the read receipt a message just marked read asks for,
// through the receipt path every surface shares. mailbox is the address of the
// mailbox the message lives in, empty for the caller's own, and is the reader the
// receipt names. The receipt is automatic, since nothing asked the reader. A
// failure is logged and never fails the read that triggered it.
func (s *Server) sendReadReceipt(st *objectstore.Store, sess *session, mailbox string, messageID int64) {
	if s.accounts == nil {
		return
	}
	reader := mailbox
	if reader == "" {
		reader = sess.user
	}
	err := mta.SendRequestedReceipt(s.accounts, s.Spool, st, messageID, reader, mta.ReceiptAutomatic, time.Now())
	if err != nil {
		s.logReceiptFailure(sess, messageID, err)
	}
}

// logReceiptFailure records a read receipt that could not be produced, naming the
// step that failed.
func (s *Server) logReceiptFailure(sess *session, messageID int64, err error) {
	stage := "unknown"
	if re, ok := errors.AsType[*mta.ReceiptError](err); ok {
		stage = re.Stage
	}
	s.Logger.Emit(logging.Event{
		Level: logging.LevelError, Subsystem: logging.EWS, Name: "readreceipt.fail",
		User:   sess.realUser,
		Fields: logging.Fields{"stage": stage, "message": messageID},
		Err:    err.Error(),
	})
}

// suppressReadReceipt answers a SuppressReadReceipt response object ([MS-OXWSMSG]
// SuppressReadReceiptType): the referenced message's receipt request is consumed
// so no later read sends it. Changing the message needs edit access to it.
func (s *Server) suppressReadReceipt(cache *storeCache, sess *session, ref refID) itemResponseMessage {
	id, err := oxews.DecodeItemID(ref.ID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	st, code := cache.openForItem(sess, id, mapi.FrightsEditAny)
	if code != "" {
		return itemError(code)
	}
	if err := mta.ConsumeReceiptRequest(st, id.MessageID); err != nil {
		return itemError("ErrorItemNotFound")
	}
	return itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", Items: &itemsWrap{}}
}
